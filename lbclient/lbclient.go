package lbclient

import (
	"context"
	. "gamblerRPC" // 带 . 的话就会在引用时可以省略前缀包名
	"io"
	"reflect"
	"sync"
)

/* 支持负载均衡的客户端 */

// LBClient 支持负载均衡的客户端
type LBClient struct {
	dis     Discovery
	mode    SelectMode
	opt     *Option
	mu      sync.Mutex
	clients map[string]*Client // 为了尽量地复用已经创建好的 Socket 连接，使用 clients 保存创建成功的 Client 实例
}

// NewLBClient 创建客户端
func NewLBClient(dis Discovery, mode SelectMode, opt *Option) *LBClient {
	return &LBClient{
		dis:     dis,
		mode:    mode,
		opt:     opt,
		clients: make(map[string]*Client),
	}
}

// Close 关闭客户端
func (xc *LBClient) Close() error {
	//TODO implement me
	xc.mu.Lock()
	defer xc.mu.Unlock()
	for key, client := range xc.clients {
		_ = client.Close()
		delete(xc.clients, key)
	}
	return nil
}

var _ io.Closer = (*LBClient)(nil)

// dial 实现对 Client 的复用
func (xc *LBClient) dial(rpcAddr string) (*Client, error) {
	xc.mu.Lock()
	defer xc.mu.Unlock()
	client, ok := xc.clients[rpcAddr]
	// 检查 xc.clients 是否有缓存的 Client
	// 如果有检查是否是可用状态，如果是则返回缓存的 Client
	// 如果不可用，则从缓存中删除
	if ok && !client.IsAvailable() {
		_ = client.Close()
		delete(xc.clients, rpcAddr)
		client = nil
	}
	// 如果步骤 1) 没有返回缓存的 Client，则说明需要创建新的 Client
	if client == nil {
		var err error
		client, err = SelectDial(rpcAddr, xc.opt)
		if err != nil {
			return nil, err
		}
		// 将创建的 client 缓存起来
		xc.clients[rpcAddr] = client
	}
	return client, nil
}

// call 实际的调用逻辑
func (xc *LBClient) call(rpcAddr string, ctx context.Context, serviceMethod string, args interface{}, reply interface{}) error {
	// 根据 rpcAddr 拿到客户端
	client, err := xc.dial(rpcAddr)
	if err != nil {
		return err
	}
	// 客户端调用 Call, 这个Call和下面的 Call 不是同一个接收者的
	return client.Call(ctx, serviceMethod, args, reply)
}

// Call 暴露给用户的调用接口， 对 call 的封装
func (xc *LBClient) Call(ctx context.Context, serviceMethod string, args interface{}, reply interface{}) error {
	// 按照传入的负载均衡策略拿到服务实例所在的机器地址
	rpcAddr, err := xc.dis.Get(xc.mode)
	if err != nil {
		return err
	}
	return xc.call(rpcAddr, ctx, serviceMethod, args, reply)
}

// Broadcast 将请求广播到所有的服务实例，如果任意一个实例发生错误，则返回其中一个错误；如果调用成功，则返回其中一个的结果
// 注意：
// 1.为了提升性能，请求是并发的。
// 2.并发情况下需要使用互斥锁保证 error 和 reply 能被正确赋值。
// 3.借助 context.WithCancel 确保有错误发生时，快速失败
func (xc *LBClient) Broadcast(ctx context.Context, serviceMethod string, args interface{}, reply interface{}) error {
	// 拿到所有的服务实例
	servers, err := xc.dis.GetAll()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup //
	var mu sync.Mutex     // 保证 error 和 reply 能被正确赋值
	var e error           //
	// 如果 reply 是 nil 则不需要设置值
	replyDone := reply == nil
	ctx, cancel := context.WithCancel(ctx)
	for _, rpcAddr := range servers {
		wg.Add(1)
		go func(rpcAddr string) {
			defer wg.Done()
			var cloneReply interface{}
			if reply != nil {
				// 拿到 reply 这个接口里面的 值 的 类型
				cloneReply = reflect.New(reflect.ValueOf(reply).Elem().Type()).Interface()
				//fmt.Printf("lbclient -> Broadcast -> %v, %v, %s\n", reflect.ValueOf(reply), reflect.ValueOf(reply).Elem(), reflect.ValueOf(reply).Elem().Type())
			}
			// 调用 call
			err := xc.call(rpcAddr, ctx, serviceMethod, args, cloneReply)
			mu.Lock()
			// 如果调用失败则使用 context.WithCancel 快速失败
			if err != nil && e == nil {
				e = err
				cancel()
			}
			// 如果调用成功并且 reply 不是 nil
			if err == nil && !replyDone {
				reflect.ValueOf(reply).Elem().Set(reflect.ValueOf(cloneReply).Elem())
				replyDone = true
			}
			mu.Unlock()
		}(rpcAddr)
	}
	wg.Wait()
	return e
}
