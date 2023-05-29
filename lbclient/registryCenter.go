package lbclient

import (
	"log"
	"net/http"
	"strings"
	"time"
)

// RegistryCenterDiscovery 嵌套了 MultiServersDiscovery，很多能力可以复用
type RegistryCenterDiscovery struct {
	*MultiServersDiscovery               // 嵌套类型，实现功能复用
	registry               string        // 注册中心的地址
	timeout                time.Duration // 服务列表的超时时间
	lastUpdate             time.Time     // 最后从注册中心更新服务列表的时间，默认10秒过期
}

// defaultUpdateTimeout 10秒后要重新从注册中心获取可用服务列表
const defaultUpdateTimeout = time.Second * 10

// NewRegistryCenterDiscovery 创建新的注册中心
func NewRegistryCenterDiscovery(registerAddr string, timeout time.Duration) *RegistryCenterDiscovery {
	if timeout == 0 {
		timeout = defaultUpdateTimeout
	}
	return &RegistryCenterDiscovery{
		registry:              registerAddr,
		timeout:               timeout,
		MultiServersDiscovery: NewMultiServersDiscovery(make([]string, 0)),
	}
}

// Update 方法更新服务列表
func (d *RegistryCenterDiscovery) Update(servers []string) error {
	//TODO implement me
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers
	d.lastUpdate = time.Now()
	return nil
}

// Refresh 超时后重新获取服务
func (d *RegistryCenterDiscovery) Refresh() error {
	//TODO implement me
	d.mu.Lock()
	defer d.mu.Unlock()
	// 如果已经超时
	if d.lastUpdate.Add(d.timeout).After(time.Now()) {
		return nil
	}
	log.Println("rpc registry: refresh servers from registry", d.registry)
	// 从注册中心地址请求服务列表
	resp, err := http.Get(d.registry)
	if err != nil {
		log.Println("rpc registry refresh err:", err)
		return err
	}
	// 拿到服务列表
	servers := strings.Split(resp.Header.Get("gamblerRPC-Server-List"), ",")
	d.servers = make([]string, 0, len(servers))
	for _, server := range servers {
		if strings.TrimSpace(server) != "" {
			d.servers = append(d.servers, strings.TrimSpace(server))
		}
	}
	d.lastUpdate = time.Now()
	return nil
}

// Get 复用 MultiServersDiscovery 的方法
func (d *RegistryCenterDiscovery) Get(mode SelectMode) (string, error) {
	//TODO implement me

	// 先调用 Refresh来确定服务列表没有过期
	if err := d.Refresh(); err != nil {
		return "", err
	}
	return d.MultiServersDiscovery.Get(mode)
}

// GetAll 复用 MultiServersDiscovery 的方法
func (d *RegistryCenterDiscovery) GetAll() ([]string, error) {
	//TODO implement me

	// 先调用 Refresh来确定服务列表没有过期
	if err := d.Refresh(); err != nil {
		return nil, err
	}
	return d.MultiServersDiscovery.GetAll()
}

// 用于检查是否实现了 Discovery 接口的所有的方法
//var _ Discovery = (*RegistryCenterDiscovery)(nil)
