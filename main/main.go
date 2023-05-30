package main

import (
	"context"
	"gamblerRPC"
	"gamblerRPC/lbclient"
	"gamblerRPC/registry"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

type Foo int

type Args struct {
	Num1 int
	Num2 int
}

// Sum 可导出的方法, 第一个参数是函数的入参，第二个参数是返回的结果，必须是指针类型才能被调用者接收到
func (f Foo) Sum(args Args, reply *int) error {
	*reply = args.Num1 + args.Num2
	return nil
}

// Sleep 用于验证 XClient 的超时机制能否正常运作。
func (f Foo) Sleep(args Args, reply *int) error {
	time.Sleep(time.Second * time.Duration(args.Num1))
	*reply = args.Num1 + args.Num2
	return nil
}

// startRegistry 启动注册中心
func startRegistry(wg *sync.WaitGroup) {
	l, _ := net.Listen("tcp", ":9999")
	registry.HandleHTTP()
	wg.Done()
	_ = http.Serve(l, nil)
}

// startServer 启动RPC服务
func startServer(registryAddr string, wg *sync.WaitGroup) {
	// 注册 Foo
	var foo Foo
	// 自动找到一个空闲的端口启动 RPC 服务
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		log.Fatal("network error:", err)
	}
	log.Println("start rpc server on", l.Addr())

	// 也可以使用 gamblerRPC.Register()，自带了创建默认的 server
	// 也可以分开使用
	server := gamblerRPC.NewServer()
	if err := server.Register(&foo); err != nil {
		log.Fatal("register error:", err)
	}

	// 使用心跳功能
	registry.HeartBeat(registryAddr, "tcp@"+l.Addr().String(), 0)

	wg.Done()
	// 把监听的地址写入管道
	//addr <- l.Addr().String()
	server.Accept(l)
}

// foo 便于在 Call 或 Broadcast 之后统一打印成功或失败的日志
func foo(xc *lbclient.LBClient, ctx context.Context, typ string, serviceMethod string, args *Args) {
	var reply int
	var err error
	switch typ {
	case "call":
		// 实际调用
		err = xc.Call(ctx, serviceMethod, args, &reply)
	case "broadcast":
		// 实际调用
		err = xc.Broadcast(ctx, serviceMethod, args, &reply)
	}
	// 打印错误或者成功信息
	if err != nil {
		log.Printf("%s %s error: %v", typ, serviceMethod, err)
	} else {
		log.Printf("%s %s success: %d + %d = %d", typ, serviceMethod, args.Num1, args.Num2, reply)
	}
}

// call 调用单个实例
func call(registry string) {
	dis := lbclient.NewRegistryCenterDiscovery(registry, 0)
	//// 对应两个机器地址，服务发现, 这里需要硬编码
	//dis := lbclient.NewMultiServersDiscovery([]string{"tcp@" + addr1, "tcp@" + addr2})
	// 采用随机选择的负载均衡策略客户端
	xc := lbclient.NewLBClient(dis, lbclient.RandomSelect, nil)
	defer func() { _ = xc.Close() }()

	// 发送请求和接收响应
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			foo(xc, context.Background(), "call", "Foo.Sum", &Args{
				Num1: i,
				Num2: i * i,
			})
		}(i)
	}
	wg.Wait()
}

// broadcast 调用所有服务实例
func broadcast(registry string) {
	dis := lbclient.NewRegistryCenterDiscovery(registry, 0)
	//dis := lbclient.NewMultiServersDiscovery([]string{"tcp@" + addr1, "tcp@" + addr2})
	xc := lbclient.NewLBClient(dis, lbclient.RandomSelect, nil)
	defer func() { _ = xc.Close() }()

	// 发送请求和接收响应
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			foo(xc, context.Background(), "broadcast", "Foo.Sum", &Args{
				Num1: i,
				Num2: i * i,
			})
			ctx, _ := context.WithTimeout(context.Background(), time.Second*2)
			foo(xc, ctx, "broadcast", "Foo.Sleep", &Args{
				Num1: i,
				Num2: i * i,
			})
		}(i)
	}
	wg.Wait()
}

func main() {
	log.SetFlags(0)
	// 启动注册中心
	registryAddr := "http://localhost:9999/_gamblerRPC_/registry"
	var wg sync.WaitGroup
	wg.Add(1)
	go startRegistry(&wg)
	wg.Wait()

	time.Sleep(time.Second)
	wg.Add(2)
	// 在任意两个地址上启动RCP服务
	go startServer(registryAddr, &wg)
	go startServer(registryAddr, &wg)
	wg.Wait()

	call(registryAddr)
	broadcast(registryAddr)

	// 反射包的使用样例
	//gamblerRPC.ReflectUseExample()

	//ch := make(chan string)
	//<-ch
}
