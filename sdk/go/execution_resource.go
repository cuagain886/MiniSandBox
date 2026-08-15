package sdk

// Execution 表示一个 sandbox 中的指定后台 execution 资源。
//
// 资源对象只保存所属 Sandbox 引用和稳定 execution ID，本身不缓存状态；
// 所有方法都在调用时向服务端查询或提交意图。
type Execution struct {
	sandbox *Sandbox
	id      string
}

// ID 返回该资源对象绑定的稳定 execution 标识。
func (e *Execution) ID() string {
	return e.id
}
