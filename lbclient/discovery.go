package lbclient

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"
)

/* 实现服务发现功能 */

// SelectMode 用来表示不同的负载均衡策略
type SelectMode int

const (
	RandomSelect     SelectMode = iota // 随机选择策略
	RoundRobinSelect                   //采用 Robin 算法的 轮询选择策略
)

// Discovery 定义接口，包含服务发现所需的最基本的接口
type Discovery interface {
	Refresh() error                      // 从注册中心更新服务列表
	Update(servers []string) error       // 手动更新服务列表
	Get(mode SelectMode) (string, error) // 根据负载均衡策略选择一个服务实例
	GetAll() ([]string, error)           // 返回所有的实例
}

// MultiServersDiscovery 我们实现一个不需要注册中心，服务列表由手工维护的服务发现的结构体
type MultiServersDiscovery struct {
	r       *rand.Rand // 采用时间戳作为随机数种子产生随机数的实例
	mu      sync.Mutex // 用于同步控制
	servers []string   // 多个服务器地址
	index   int        // 记录 Round Robin 算法已经轮询到的位置，为了避免每次从 0 开始
}

// Refresh 从注册中心更新服务列表
func (m *MultiServersDiscovery) Refresh() error {
	//TODO implement me
	return nil
}

// Update 手动更新服务列表
func (m *MultiServersDiscovery) Update(servers []string) error {
	//TODO implement me
	m.mu.Lock()
	defer m.mu.Unlock()
	m.servers = servers
	return nil
}

// Get 根据负载均衡策略选择一个服务实例
func (m *MultiServersDiscovery) Get(mode SelectMode) (string, error) {
	//TODO implement me
	m.mu.Lock()
	defer m.mu.Unlock()
	// 获取服务个数
	n := len(m.servers)
	if n == 0 {
		return "", errors.New("rpc discovery: no available servers")
	}
	switch mode {
	case RandomSelect:
		// Intn(n) 限定范围 0 - n
		return m.servers[m.r.Intn(n)], nil
	case RoundRobinSelect:
		//服务器可以更新，所以模式n以确保安全
		s := m.servers[m.index%n]
		m.index = (m.index + 1) % n
		// 看下 s 到底是什么
		fmt.Printf("discovery.gp -> Get -> s = %s", s)
		return s, nil
	default:
		return "", errors.New("rpc discovery: not supported select mode")
	}
}

// GetAll 返回所有的实例
func (m *MultiServersDiscovery) GetAll() ([]string, error) {
	//TODO implement me
	m.mu.Lock()
	defer m.mu.Unlock()
	// 返回一个 servers 的副本
	servers := make([]string, len(m.servers), len(m.servers))
	copy(servers, m.servers)
	return servers, nil
}

// 检查 MultiServerDiscovery 是否实现了 _Discovery 的全部方法
var _ Discovery = (*MultiServersDiscovery)(nil)

func NewMultiServersDiscovery(servers []string) *MultiServersDiscovery {
	// servers 类似 tcp@ip.port的形式，也就是 RPC链接服务器的格式
	m := &MultiServersDiscovery{
		servers: servers,
		r:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	m.index = m.r.Intn(math.MaxInt32 - 1)
	return m
}
