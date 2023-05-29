package gamblerRPC

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gamblerRPC/codec"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Call 封装结构体保存一次 RPC 请求调用所需信息
type Call struct {
	Seq           uint64      // 用于区分请求，相当于请求的 ID
	ServiceMethod string      // 用于保存请求的服务名和方法名， format "<service>.<method>"
	Args          interface{} // 用于保存请求的参数
	Reply         interface{} // 用于保存响应的数据
	Error         error       // 发生错误时返回错误信息
	Done          chan *Call  // 支持异步调用，当调用结束时使用 call.done() 通知调用方
}

// done 结束调用时通知调用方
func (call *Call) done() {
	// Done 是一个 *Call 类型的 chan，这里是调用结束时把 call 写入到 chan 中
	call.Done <- call
}

// 客户端设计及基础方法 ↓

// Client 表示一个 RPC 客户端，可能有多个与单个 Client 相关联的未完成调用，并且一个 Client 可能同时被多个 goroutines 使用
type Client struct {
	cc       codec.Codec      // 不同的 codec 实例，比如针对gob的消息编码器和解码器或者是针对其他类型的
	opt      *Option          // 确定消息的编码方式
	sending  sync.Mutex       // 保证请求的有序发送
	header   codec.Header     // 每个请求的消息头，header 只有在请求发送时才需要，而请求发送是互斥的，因此每个客户端只需要一个
	mu       sync.Mutex       // 用于每个客户端的并发控制
	seq      uint64           // 区分不同的请求，相当于请求的 ID
	pending  map[uint64]*Call // 存储没有处理完的请求，键是请求的编号，值是请求
	closing  bool             // closing 为 true 是用户主动关闭的
	shutdown bool             // shutdown 为 true 一般是有错误发生
}

// 检查 *Client 是否实现了 io.Closer 的全部接口
var _ io.Closer = (*Client)(nil)

// ErrShutdown 定义一个错误反馈信息
var ErrShutdown = errors.New("connection is shut down")

// Close 实现 io.Closer 的 接口
func (client *Client) Close() error {
	//TODO implement me
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing {
		return ErrShutdown
	}
	client.closing = true
	return client.cc.Close()
}

// IsAvailable 检查客户端是否可用
func (client *Client) IsAvailable() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return !client.shutdown && !client.closing
}

// registerCall 将参数 call 添加到 client.pending 中，并更新 client.seq
func (client *Client) registerCall(call *Call) (uint64, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing || client.shutdown {
		return 0, ErrShutdown
	}
	call.Seq = client.seq
	client.pending[call.Seq] = call
	client.seq++
	return call.Seq, nil
}

// removeCall 根据 seq，从 client.pending 中移除对应的 call，并返回
func (client *Client) removeCall(seq uint64) *Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	call := client.pending[seq]
	delete(client.pending, seq)
	return call
}

// terminateCalls 服务端或客户端发生错误时调用，将 shutdown 设置为 true，且将错误信息通知所有 pending 状态的 call
func (client *Client) terminateCalls(err error) {
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()

	client.shutdown = true
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

/* 对一个客户端端来说，接收响应、发送请求是最重要的 2 个功能 */

/* 1、实现接收响应的功能 */

// receive 处理接收到的响应
func (client *Client) receive() {
	var err error
	for err == nil {
		var h codec.Header
		if err = client.cc.ReadHeader(&h); err != nil {
			break
		}
		call := client.removeCall(h.Seq)

		switch {
		// call 不存在，可能是请求没有发送完整，或者因为其他原因被取消，但是服务端仍旧处理了
		case call == nil:
			err = client.cc.ReadBody(nil)
		// call 存在，但服务端处理出错，即 h.Error 不为空。
		case h.Error != "":
			call.Error = fmt.Errorf(h.Error)
			err = client.cc.ReadBody(nil)
			call.done()
		// call 存在，服务端处理正常，那么需要从 body 中读取 Reply 的值
		default:
			err = client.cc.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading body " + err.Error())
			}
			call.done()
		}
	}
	// 发生了错误
	client.terminateCalls(err)
}

/* 2、实现发送请求的功能 */

// send 发送请求
func (client *Client) send(call *Call) {
	// 保证客户端发送一个完整的请求
	client.sending.Lock()
	defer client.sending.Unlock()

	// 注册一个请求
	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}

	// 准备请求头
	client.header.ServiceMethod = call.ServiceMethod
	client.header.Seq = call.Seq
	client.header.Error = ""

	// 编码并且发送请求
	if err := client.cc.Write(&client.header, call.Args); err != nil {
		call := client.removeCall(seq)
		if call != nil {
			// call 可能为 nil，通常表示 Write 部分失败
			// 客户已收到响应并处理
			call.Error = err
			call.done()
		}
	}
}

// GenerateCall 异步接口，返回一个 call 实例
func (client *Client) GenerateCall(serviceMethod string, args interface{}, reply interface{}, done chan *Call) *Call {
	// 如果没有传入管道则创建，管道也不能是同步的
	if done == nil {
		done = make(chan *Call, 10)
	} else if cap(done) == 0 {
		log.Panic("rpc client: done channel is unbuffered")
	}

	// 创建请求并发送
	call := &Call{
		ServiceMethod: serviceMethod,
		Args:          args,
		Reply:         reply,
		Done:          done,
	}
	client.send(call)
	return call
}

// Call 是对 GenerateCall 的封装，阻塞 call.Done，等待响应返回，是一个同步接口
func (client *Client) Call(ctx context.Context, serviceMethod string, args interface{}, reply interface{}) error {
	call := client.GenerateCall(serviceMethod, args, reply, make(chan *Call, 1))
	// context 包主要是⽤来处理多个 goroutine 之间共享数据，及多个 goroutine 的管理
	// Done() 返回⼀个只能接受数据的channel类型，当该 context 关闭或者超时时间到了的时候，该 channel 就会有⼀个取消信号
	// Err() 在Done() 之后，返回context 取消的原因
	// Deadline() 设置该context cancel的时间点
	// Value() ⽅法允许 Context 对象携带request作⽤域的数据，该数据必须是线程安全的
	// Context 对象是线程安全的，可以把⼀个 Context 对象传递给任意个数的 goroutine，对它执⾏ 取消 操作时，所有 goroutine 都会接收到取消信号
	select {
	// 超时后会收到一个取消的信号
	case <-ctx.Done():
		client.removeCall(call.Seq)
		return errors.New("rpc client: call failed: " + ctx.Err().Error())
	// 正常调用
	case call := <-call.Done:
		return call.Error
	}
}

/* 创建客户端 */

type clientResult struct {
	client *Client
	err    error
}

// newClientFunc 用于在 dialTimeout 中包裹 newClient 函数
type newClientFunc func(conn net.Conn, opt *Option) (client *Client, err error)

// NewHTTPClient 实例化一个支持 HTTP 传输协议的客户端
func NewHTTPClient(conn net.Conn, opt *Option) (*Client, error) {
	// 向 conn 中写入请求的信息内容
	_, _ = io.WriteString(conn, fmt.Sprintf("CONNECT %s HTTP/1.0\n\n", defaultRPCPath))
	// 利用建立的 TCP 连接 conn 发送一个 CONNECT 方式的 http 请求，服务端将会对 conn 进行改造并返回响应
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	// 如果成功收到响应，那么表示 HTTP 连接已经建立，此时 rpc 的请求可以通过 HTTP 的请求方式进行传递
	if err == nil && resp.Status == connected {
		return NewClient(conn, opt)
	}
	// 得到了响应但是响应状态不是要求的 connected = "200 Connected to gamblerRPC"
	if err == nil {
		err = errors.New("unexpected HTTP response: " + resp.Status)
	}
	return nil, err
}

// DialHTTP 连接到位于指定网络地址的 HTTP RPC 服务器并监听默认的 HTTP RPC 路径。
func DialHTTP(network string, address string, opts ...*Option) (*Client, error) {
	// 通过 HTTP CONNECT 请求建立连接之后，后续的通信过程就交给 NewClient 了
	return dialTimeout(NewHTTPClient, network, address, opts...)
}

// NewClient 创建新的客户端
func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", opt.CodecType)
		log.Println("rpc client: codec error:", err)
		return nil, err
	}
	// 发送 Option 给服务端
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client: options error: ", err)
		_ = conn.Close()
		return nil, err
	}
	return newClientCodec(f(conn), opt), nil
}

// Dial 连接到指定网络地址的 RPC 服务器, 简化用户调用将 Option 实现为可选参数
func Dial(network, address string, opts ...*Option) (client *Client, err error) {
	return dialTimeout(NewClient, network, address, opts...)
}

// newClientCodec 确定编码方式，以使用对应的编码解码器
func newClientCodec(cc codec.Codec, opt *Option) *Client {
	client := &Client{
		seq:     1,
		cc:      cc,
		opt:     opt,
		pending: make(map[uint64]*Call),
	}
	// 协商好消息的编解码方式之后，再创建一个子协程调用 receive() 接收响应
	go client.receive()
	return client
}

// dialTimeout 带有连接超时处理的客户端
func dialTimeout(f newClientFunc, network string, address string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	// 执行网络连接地址以及连接超时时间，这里是 TCP 连接，因为传入的 network 字段是 tcp
	conn, err := net.DialTimeout(network, address, opt.ConnectionTimeout)
	if err != nil {
		return nil, err
	}
	// 关闭连接
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()
	// 创建 clientResult 类型的管道
	ch := make(chan clientResult)
	// 使用子协程执行 NewClient，执行完成后则通过信道 ch 发送结果
	go func() {
		// f 是 NewClient 函数，将 TCP 连接传递给创建客户端的方法如 NewHTTPClient
		client, err := f(conn, opt)
		ch <- clientResult{
			client: client,
			err:    err,
		}
	}()
	// 如果没有超时时间限制则直接返回客户端
	if opt.ConnectionTimeout == 0 {
		result := <-ch
		return result.client, result.err
	}
	// 如果有超时时间则按照是否超时返回结果
	select {
	// time.After() 信道先接收到消息，则说明 NewClient 执行超时，返回错误
	case <-time.After(opt.ConnectionTimeout):
		return nil, fmt.Errorf("rpc client: connect timeout: expect within %s", opt.ConnectionTimeout)
	case result := <-ch:
		return result.client, result.err
	}
}

// parseOptions 解析 Option 的字段
func parseOptions(opts ...*Option) (*Option, error) {
	// 如果没有 opt 那就设置为默认的 opt
	if len(opts) == 0 || opts[0] == nil {
		return DefaultOption, nil
	}
	if len(opts) != 1 {
		return nil, errors.New("number of options is more than 1")
	}
	opt := opts[0]
	opt.MagicNumber = DefaultOption.MagicNumber
	if opt.CodecType == "" {
		opt.CodecType = DefaultOption.CodecType
	}
	return opt, nil
}

// SelectDial 统一调用入口
// SelectDial 根据第一个参数 rpcAddr 调用不同的函数来连接到 RPC 服务器。 rpcAddr 是表示 rpc 服务器的通用格式 (protocol@addr)
// 例如，http@10.0.0.1:7001, tcp@10.0.0.1:9999, unix@/tmp/gamblerRPC.sock
func SelectDial(rpcAddr string, opts ...*Option) (*Client, error) {
	parts := strings.Split(rpcAddr, "@")
	if len(parts) != 2 {
		return nil, fmt.Errorf("rpc client err: wrong format '%s', expect protocol@addr", rpcAddr)
	}
	// 拿到协议和地址
	protocol, addr := parts[0], parts[1]
	// 根据不同的协议选择不同的连接方式
	switch protocol {
	case "http":
		return DialHTTP("tcp", addr, opts...)
	default:
		return Dial(protocol, addr, opts...)
	}
}
