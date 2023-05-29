package registry

import (
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type Registry struct {
	timeout time.Duration          // 设置超时时间
	mu      sync.Mutex             // 同步控制
	servers map[string]*ServerItem // 服务的信息，包括服务所在的地址和启动时间等
}

// ServerItem 服务的信息
type ServerItem struct {
	Addr  string    // 服务存在于哪一个服务端
	start time.Time // 开始时间
}

// 设置默认的一些量
const (
	defaultRegistryPath = "/_gamblerRPC_/registry"
	defaultTimeout      = time.Minute * 5
)

// New 创建注册中心
func New(timeout time.Duration) *Registry {
	return &Registry{
		timeout: timeout,
		servers: make(map[string]*ServerItem),
	}
}

// DefaultRegistry 默认的超时时间是5分钟
var DefaultRegistry = New(defaultTimeout)

// putServer 给注册中心添加服务实例，如果已经存在则更新 start 时间
func (r *Registry) putServer(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.servers[addr]
	if s == nil {
		// 如果不存在则创建一个服务
		r.servers[addr] = &ServerItem{Addr: addr, start: time.Now()}
	} else {
		// 如果存在则更新 start 时间
		s.start = time.Now()
	}
}

// aliveServers 返回目前可用的服务列表，删除超时的服务
func (r *Registry) aliveServers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	// 列表保存的是可用服务端的 addr
	var alive []string
	for addr, server := range r.servers {
		// 如果不设超时时间或者没有过期
		if r.timeout == 0 || server.start.Add(r.timeout).After(time.Now()) {
			alive = append(alive, addr)
		} else {
			// 过期就删除这个服务端地址
			delete(r.servers, addr)
		}
	}
	sort.Strings(alive)
	return alive
}

// ServeHTTP 采用 HTTP 提供服务，所有的信息放在 Header 中
func (r *Registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case "GET":
		// Get：返回所有可用的服务列表，通过自定义字段 gamblerRPC-Servers 承载
		w.Header().Set("gamblerRPC-Server-List", strings.Join(r.aliveServers(), ","))
	case "POST":
		// Post：添加服务实例或发送心跳，通过自定义字段 gamblerRPC-Servers 承载
		addr := req.Header.Get("gamblerRPC-Server-Alive")
		if addr == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		r.putServer(addr)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// HandleHTTP 设置提供注册中心服务的路径
func (r *Registry) HandleHTTP(registryPath string) {
	http.Handle(registryPath, r)
	log.Println("rpc registry path:", registryPath)
}

// HandleHTTP 采用默认的超时时间和路径设置注册中心的服务路径
func HandleHTTP() {
	DefaultRegistry.HandleHTTP(defaultRegistryPath)
}

// HeartBeat 每隔一段时间发送心跳消息给注册中心证明服务是可以正常提供服务的
func HeartBeat(registry string, addr string, duration time.Duration) {
	// 如果发送心跳消息的时间间隔是 0 则默认设置为 比服务过期时间少一分钟。
	if duration == 0 {
		// 保证在服务从注册中心移除之前有足够的时间发送心跳消息
		duration = defaultTimeout - time.Duration(1)*time.Second
		//duration = time.Duration(1) * time.Second
	}
	var err error
	err = sendHeartBeat(registry, addr)
	// 首次发送心跳成功后启动协程每隔 duration 发送一次心跳
	go func() {
		t := time.NewTicker(duration)
		// 只要第一次成功发送心跳消息后续就每隔一定时间发送心跳消息
		for err == nil {
			//从管道中读取
			<-t.C
			err = sendHeartBeat(registry, addr)
		}
	}()
}

// sendHeartBeat 发送心跳消息
func sendHeartBeat(registry string, addr string) error {
	log.Println(addr, "send heart beat to registry", registry)
	// 创建一个 http 客户端，并发起一次 POST 请求
	httpClient := &http.Client{}
	// 请求中使用 gamblerRPC-Servers 字段携带当前服务的 addr 证明当前服务是可用的
	req, _ := http.NewRequest("POST", registry, nil)
	req.Header.Set("gamblerRPC-Server-Alive", addr)
	if _, err := httpClient.Do(req); err != nil {
		log.Println("rpc server: heart beat err:", err)
		return err
	}
	return nil
}
