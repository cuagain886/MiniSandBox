// Package metrics 定义 MiniSandbox 独立 Prometheus registry 与低基数业务指标实现。
// 本包不使用全局 registry、不默认注册 Go/process collector，也不负责 HTTP 暴露或 Store 查询。
package metrics

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// Registry 持有单个服务实例独享的 collector 注册表。
type Registry struct {
	registry *prometheus.Registry
}

// NewRegistry 创建没有任何默认 collector 的独立 registry。
func NewRegistry() *Registry {
	return &Registry{registry: prometheus.NewRegistry()}
}

// Register 注册一个 collector；重复或 descriptor 冲突会返回错误且不会触发 panic。
func (registry *Registry) Register(collector prometheus.Collector) error {
	if registry == nil || registry.registry == nil || collector == nil {
		return fmt.Errorf("register metrics collector: invalid dependency")
	}
	return registry.registry.Register(collector)
}

// Gatherer 返回只读采集端口，供测试以及后续受保护的 metrics handler 注入使用。
func (registry *Registry) Gatherer() prometheus.Gatherer {
	if registry == nil {
		return nil
	}
	return registry.registry
}
