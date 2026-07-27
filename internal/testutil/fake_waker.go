package testutil

import "sync"

// FakeWaker 是线程安全、可模拟通知丢失的 Waker 测试替身。
type FakeWaker struct {
	mu sync.Mutex

	deliver   bool
	calls     []string
	delivered []string
}

// NewFakeWaker 创建默认接受通知的 Waker 替身。
func NewFakeWaker() *FakeWaker {
	return &FakeWaker{deliver: true}
}

// SetDeliver 配置后续 Wake 是否被记录为已投递。
//
// 无论是否投递，WakeCalls 都会保留调用尝试，便于区分 service 未调用和队列
// 因关闭而丢弃。
func (f *FakeWaker) SetDeliver(deliver bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliver = deliver
}

// Wake 记录通知尝试，并在 deliver 启用时记录实际投递。
func (f *FakeWaker) Wake(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id)
	if f.deliver {
		f.delivered = append(f.delivered, id)
	}
}

// WakeCalls 返回全部 Wake 尝试的独立快照。
func (f *FakeWaker) WakeCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// Delivered 返回被 fake 接受投递的 sandbox ID 独立快照。
func (f *FakeWaker) Delivered() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.delivered...)
}
