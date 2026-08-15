package sdk

import "context"

// CreateOption 是 Client.Create 支持的可选创建语义。
type CreateOption func(*createOptions)

// createOptions 汇总 CreateOption 展开后的可选语义。
type createOptions struct {
	idempotencyKey string
}

// WithIdempotencyKey 让 Create 在当前 local:v1 scope 内可安全重放。
//
// 同一 key 加同一请求会重放首次 202 结果；同一 key 加不同请求返回 409。
// key 本身必须满足公共协议的 1 到 128 位受限 ASCII 字符要求。
func WithIdempotencyKey(key string) CreateOption {
	return func(options *createOptions) {
		options.idempotencyKey = key
	}
}

// Create 提交 sandbox 创建请求并直接返回绑定该 sandbox 的资源对象。
//
// 本方法在 CreateSandboxWithOptions 之上只做资源对象包装，请求校验和
// HTTP 语义完全复用底层实现。
func (c *Client) Create(
	ctx context.Context,
	request CreateSandboxRequest,
	options ...CreateOption,
) (*Sandbox, error) {
	var resolved createOptions
	for _, option := range options {
		option(&resolved)
	}
	sandbox, err := c.CreateSandboxWithOptions(ctx, request, CreateSandboxOptions{
		IdempotencyKey: resolved.idempotencyKey,
	})
	if err != nil {
		return nil, err
	}
	return &Sandbox{client: c, id: sandbox.ID}, nil
}

// Sandbox 用已知 ID 构造 sandbox 资源对象。
//
// 本方法不发起任何请求；ID 不存在时错误发生在后续第一次查询。
func (c *Client) Sandbox(sandboxID string) *Sandbox {
	return &Sandbox{client: c, id: sandboxID}
}

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
