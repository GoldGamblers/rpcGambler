package gamblerRPC

import (
	"context"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

type Bar int

// Timeout 模拟耗时 2S
func (b Bar) Timeout(argv int, reply *int) error {
	time.Sleep(time.Second * 2)
	return nil
}

// startServer 启动服务并注册
func startServer(addr chan string) {
	// 注册 Bar 的实例 b 为服务
	var b Bar
	_ = Register(&b)
	// 选择一个空闲端口
	l, _ := net.Listen("tcp", ":0")
	addr <- l.Addr().String()
	Accept(l)
}

func TestClient_Call(t *testing.T) {
	t.Parallel()
	addrCh := make(chan string)
	go startServer(addrCh)
	addr := <-addrCh
	time.Sleep(time.Second)

	// 客户端设置超时时间为 1s，服务端无限制
	t.Run("client timeout", func(t *testing.T) {
		client, _ := Dial("tcp", addr)
		// 用户实现对 Dial 的超时控制
		ctx, _ := context.WithTimeout(context.Background(), time.Second)
		var reply int
		err := client.Call(ctx, "Bar.Timeout", 1, &reply)
		_assert(err != nil && strings.Contains(err.Error(), ctx.Err().Error()), "expect a timeout error")
	})
	// 服务端设置超时时间为1s，客户端无限制
	t.Run("server handle timeout", func(t *testing.T) {
		client, _ := Dial("tcp", addr, &Option{
			HandleTimeout: time.Second,
		})
		var reply int
		err := client.Call(context.Background(), "Bar.Timeout", 1, &reply)
		_assert(err != nil && strings.Contains(err.Error(), "handle timeout"), "expect a timeout error")
	})
}

func TestXDial(t *testing.T) {
	if runtime.GOOS == "linux" {
		ch := make(chan struct{})
		addr := "/tmp/gamblerRPC.sock"
		go func() {
			_ = os.Remove(addr)
			l, err := net.Listen("tcp", addr)
			if err != nil {
				t.Fatal("failed to listen unix socket")
			}
			ch <- struct{}{}
			Accept(l)
		}()
		<-ch
		_, err := SelectDial("unix@" + addr)
		_assert(err == nil, "failed to connect unix socket")
	}
}
