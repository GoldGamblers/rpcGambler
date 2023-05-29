package gamblerRPC

import (
	"encoding/json"
	"errors"
	"fmt"
	"gamblerRPC/codec"
	"io"
	"log"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
)

// MagicNumber ?
const MagicNumber = 0x3bef5c
const connected = "200 Connected to gamblerRPC"
const defaultRPCPath = "/_gamblerRPC_"
const defaultDebugPath = "/debug/gamblerRPC"

// Option 协商信息
type Option struct {
	MagicNumber       int           // 用于标记 RPC 的请求
	CodecType         codec.Type    // 客户端可能会选择不用的 Codec 去编码，这里只实现的 gob
	ConnectionTimeout time.Duration // 连接超时，0 代表不设限制
	HandleTimeout     time.Duration // 处理超时
}

// DefaultOption 默认协商信息
var DefaultOption = &Option{
	MagicNumber:       MagicNumber,
	CodecType:         codec.GobType,
	ConnectionTimeout: time.Second * 10, // 连接超时默认设置为 10秒
}

// 一般来说，涉及协议协商的这部分信息，需要设计固定的字节来传输的，这里为了实现简单采用如下方式
//  客户端固定采用 JSON 编码 Option，后续的 header 和 body 的编码方式由 Option 中的 CodeType 指定
//  服务端首先使用 JSON 解码 Option，然后通过 Option 的 CodeType 解码剩余的内容

// Server 定义一个 Server RPC服务对象
type Server struct {
	serviceMap sync.Map
}

// Register 注册在服务器上发布的方法
func (server *Server) Register(rcvr interface{}) error {
	s := newService(rcvr)
	// 注册结构体时检查是否已经注册了
	if _, dup := server.serviceMap.LoadOrStore(s.name, s); dup {
		return errors.New("rpc: service already defined: " + s.name)
	}
	return nil
}

// Register 在 DefaultServer 中发布接收者的方法。
func Register(rcvr interface{}) error { return DefaultServer.Register(rcvr) }

// findService 通过 ServiceMethod 从 serviceMap 找到对应的 service
func (server *Server) findService(serviceMethod string) (svc *service, mtype *methodType, err error) {
	// 检查 serviceMethod 中 . 的位置
	dot := strings.LastIndex(serviceMethod, ".")
	if dot < 0 {
		err = errors.New("rpc server: service/method request ill-formed: " + serviceMethod)
		return
	}
	//serviceMethod = serviceName.methodName
	serviceName, methodName := serviceMethod[:dot], serviceMethod[dot+1:]
	// 通过服务名获取对应的服务实例，也就是结构体对应的服务
	svci, ok := server.serviceMap.Load(serviceName)
	if !ok {
		err = errors.New("rpc server: can't find service " + serviceName)
		return
	}
	// 返回结果
	svc = svci.(*service)
	// 从服务实例中通过 methodName 拿到方法的信息
	mtype = svc.method[methodName]
	if mtype == nil {
		err = errors.New("rpc server: can't find method " + methodName)
	}
	return
}

// NewServer 返回一个新的 Server
func NewServer() *Server {
	return &Server{}
}

// DefaultServer 默认的 serever 实例
var DefaultServer = NewServer()

// Accept for 循环等待 socket 连接建立，并开启子协程处理，处理过程交给了 ServerConn 方法
func (server *Server) Accept(lis net.Listener) {
	for {
		conn, err := lis.Accept()
		if err != nil {
			log.Println("rpc server: accept error:", err)
			return
		}
		// 开启协程处理
		go server.ServerConn(conn)
	}
}

// Accept 采用默认的 Server
func Accept(lis net.Listener) { DefaultServer.Accept(lis) }

// ServerConn 首先使用 json.NewDecoder 反序列化得到 Option 实例，检查 MagicNumber 和 CodeType 的值是否正确
// 然后根据 CodeType 得到对应的消息编解码器，接下来的处理交给 serverCodec
func (server *Server) ServerConn(conn io.ReadWriteCloser) {
	defer func() { _ = conn.Close() }()

	// 通过反序列化得到Option实例保存在 opt 中
	var opt Option
	// 把 conn 中的 opt 解析到 定义的 opt 中
	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("rpc server: options error: ", err)
		return
	}
	// 检查 MagicNumber 和 CodeType 的值是否正确
	if opt.MagicNumber != MagicNumber {
		log.Printf("rpc server: invalid magic number %x", opt.MagicNumber)
		return
	}
	// 根据 CodeType 得到对应的消息编解码器，接下来的处理交给 serverCodec
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		log.Printf("rpc server: invalid codec type %s", opt.CodecType)
		return
	}
	server.serveCodec(f(conn), &opt)
}

// invalidRequest 发生错误时响应 argv 的占位符
var invalidRequest = struct{}{}

// serveCodec 主要包含 读取、处理、回复请求三个阶段
// 允许接收多个请求，即多个 request header 和 request body
// 因此这里使用了 for 无限制地等待请求的到来，直到发生错误（例如连接被关闭，接收到的报文有问题等）
// 注意：handleRequest 使用了协程并发执行请求、
// 注意：处理请求是并发的，但是回复请求的报文必须是逐个发送的，并发容易导致多个回复报文交织在一起，客户端无法解析。在这里使用锁(sending)保证
// 注意：只有在 header 解析失败时，才终止循环
func (server *Server) serveCodec(cc codec.Codec, opt *Option) {
	// 用于并发控制
	// 确定发送一个完整的响应
	sending := new(sync.Mutex)
	// 等待所有的请求都被处理
	wg := new(sync.WaitGroup)

	for {
		// 读取请求
		req, err := server.readRequest(cc)
		// 读取失败
		if err != nil {
			if req == nil {
				// 无法实现 recover 所以直接跳出循环关闭连接
				break
			}
			req.h.Error = err.Error()
			// 回复请求
			server.sendResponse(cc, req.h, invalidRequest, sending)
			continue
		}
		// 读取成功后处理请求
		wg.Add(1)
		go server.handleRequest(cc, req, sending, wg, opt.HandleTimeout)
	}
	wg.Wait()
	_ = cc.Close()
}

// 请求的body， 存储一个调用的全部因袭
type request struct {
	h            *codec.Header // 请求的 Header 部分，包括服务名、方法名等
	argv, replyv reflect.Value // 请求的 参数 and 回复值，是 body 部分
	mtype        *methodType   // 请求的方法实例
	svc          *service      // 请求的结构体实例
}

// readRequestHeader 读取请求的 Header
func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	var h codec.Header
	// 把请求中的 请求头 读取到 h 中
	if err := cc.ReadHeader(&h); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read header error:", err)
		}
		return nil, err
	}
	return &h, nil
}

// readRequest 读取请求
func (server *Server) readRequest(cc codec.Codec) (*request, error) {
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}
	req := &request{h: h}
	// 请求头中包含了 ServiceMethod，这部分的功能就是根据请求的参数类型来构造对应的参数实例
	req.svc, req.mtype, err = server.findService(h.ServiceMethod)
	if err != nil {
		return req, err
	}

	// 创建参数和返回值
	req.argv = req.mtype.newArgv()
	req.replyv = req.mtype.newReplyv()

	// argv 必须是指针，读 body 需要指针
	argvi := req.argv.Interface()
	if req.argv.Type().Kind() != reflect.Pointer {
		argvi = req.argv.Addr().Interface()
	}
	// 读请求体，将请求的参数反序列化
	if err = cc.ReadBody(argvi); err != nil {
		log.Println("rpc server: read argv err:", err)
	}
	return req, nil
}

// sendResponse 回复请求
func (server *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex) {
	sending.Lock()
	defer sending.Unlock()
	// 把回复使用 cc 的 write 写入到缓冲区，conn 从缓冲区中读数据
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error:", err)
	}
}

// handleRequest 处理请求
func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
	// 并发控制，相当于计数器，执行前加1，执行后减1
	defer wg.Done()
	// 这里需要确保 sendResponse 仅调用一次
	// 因此将整个过程拆分为 called 和 sent 两个阶段
	called := make(chan struct{})
	sent := make(chan struct{})
	go func() {
		err := req.svc.call(req.mtype, req.argv, req.replyv)
		// call 了一次就向 called 管道写入 struct{}{}
		called <- struct{}{}
		// call 发生了错误
		if err != nil {
			req.h.Error = err.Error()
			server.sendResponse(cc, req.h, invalidRequest, sending)
			// sendResponse 一次就向 sent 管道写入 struct{}{}
			sent <- struct{}{}
			return
		}
		// call 没发生错误
		server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
		sent <- struct{}{}
	}()
	// 不设置超时时间
	if timeout == 0 {
		<-called
		<-sent
		return
	}
	// 设置了超时时间
	select {
	// time.After() 先于 called 接收到消息，说明处理已经超时,called 和 sent 都将被阻塞
	case <-time.After(timeout):
		req.h.Error = fmt.Sprintf("rpc server: request handle timeout: expect within %s", timeout)
		server.sendResponse(cc, req.h, invalidRequest, sending)
	// called 信道接收到消息，代表处理没有超时
	case <-called:
		<-sent
	}
}

/* 增加对 HTTP 协议的支持 */

// ServeHTTP 实现了一个响应 RPC 请求的 http.Handler。
func (server *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// 不是 CONNECT 方法
	if req.Method != "CONNECT" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, "405 must CONNECT\n")
		return
	}
	// 默认情况下连接的关闭是Server来控制的，但是HttpServer提供了一个Hijacker的接口可以让开发者来接管连接状态的管理
	// 如果一个连接被Hijack了，那么连接的关闭需要由开发者自己管理
	// 意义在于：服务端返回的内容可以不符合http协议了，例如只包含body的内容，并没有http协议行和协议头部分
	// 这也是基于 HTTP 实现 RPC 的一个基本场景
	// 该场景下RPC的网络通讯层使用http协议，Rpc Server可以复用http server的连接建立、请求处理的流程
	// 然后在Handler内使用Hijack方法拿到连接，然后就可以做Rpc服务端该做的事件就可以了

	// Hijacker 接口由 ResponseWriters 实现，它允许 HTTP 处理程序接管这个连接，这个 HTTP 处理程序是用户自定义的 handleFunc
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		log.Print("rpc hijacking ", req.RemoteAddr, ": ", err.Error())
		return
	}
	_, _ = io.WriteString(conn, "HTTP/1.0 "+connected+"\n\n")
	server.ServerConn(conn)
}

// HandleHTTP 为 rpcPath 上的 RPC 消息注册 HTTP 处理程序, 仍然需要调用 http.Serve()
func (server *Server) HandleHTTP() {
	// 这里用的是 RPC 服务端的 ServeHTTP，在这个 ServeHTTP 中将 conn 使用 HTTP 处理程序接管
	http.Handle(defaultRPCPath, server)
	log.Println("rpc server path:", defaultRPCPath)
	// 这里用的是 debugHTTP 的 ServeHTTP，在这个 ServeHTTP 中将服务的信息渲染到 HTML 页面并返回
	http.Handle(defaultDebugPath, debugHTTP{server})
	log.Println("rpc server debug path:", defaultDebugPath)
}

// HandleHTTP 是默认服务器注册 HTTP 处理程序的便捷方法
func HandleHTTP() {
	DefaultServer.HandleHTTP()
}
