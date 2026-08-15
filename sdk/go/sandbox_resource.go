package sdk

// Sandbox 表示控制面中的一个 sandbox 资源。
//
// 资源对象只保存 client 引用和稳定 sandbox ID，本身不缓存状态；所有方法
// 都在调用时向服务端查询或提交意图，因此可以安全地跨 goroutine 复用。
type Sandbox struct {
	client *Client
	id     string
}

// ID 返回该资源对象绑定的稳定 sandbox 标识。
func (s *Sandbox) ID() string {
	return s.id
}
