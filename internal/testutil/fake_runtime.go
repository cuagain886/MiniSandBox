package testutil

import (
	"context"
	"sync"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
)

// FakeRuntime 是线程安全、可注入结果并记录调用的 Runtime 测试替身。
type FakeRuntime struct {
	mu sync.Mutex

	ensureResult runtimeport.ActualSandbox
	ensureErr    error
	ensureCalls  []domain.Sandbox

	inspectResult runtimeport.ActualSandbox
	inspectErr    error
	inspectCalls  []string

	deleteErr   error
	deleteCalls []string

	listManagedResult []runtimeport.ActualSandbox
	listManagedErr    error
	listManagedCalls  int
}

// NewFakeRuntime 创建所有方法默认成功并返回零值结果的 Runtime 替身。
func NewFakeRuntime() *FakeRuntime {
	return &FakeRuntime{}
}

// SetEnsureResult 配置 Ensure 返回的实际状态和错误。
func (f *FakeRuntime) SetEnsureResult(result runtimeport.ActualSandbox, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureResult = result
	f.ensureErr = err
}

// EnsureCalls 返回 Ensure 接收的 sandbox 独立快照。
func (f *FakeRuntime) EnsureCalls() []domain.Sandbox {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Sandbox(nil), f.ensureCalls...)
}

// Ensure 记录 sandbox 并返回预先配置的实际状态。
func (f *FakeRuntime) Ensure(
	_ context.Context,
	sandbox domain.Sandbox,
) (runtimeport.ActualSandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureCalls = append(f.ensureCalls, sandbox)
	return f.ensureResult, f.ensureErr
}

// SetInspectResult 配置 Inspect 返回的实际状态和错误。
func (f *FakeRuntime) SetInspectResult(result runtimeport.ActualSandbox, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspectResult = result
	f.inspectErr = err
}

// InspectCalls 返回 Inspect 接收的 ID 独立快照。
func (f *FakeRuntime) InspectCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.inspectCalls...)
}

// Inspect 记录 ID 并返回预先配置的实际状态。
func (f *FakeRuntime) Inspect(
	_ context.Context,
	id string,
) (runtimeport.ActualSandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspectCalls = append(f.inspectCalls, id)
	return f.inspectResult, f.inspectErr
}

// SetDeleteError 配置 Delete 返回的错误；传入 nil 恢复成功。
func (f *FakeRuntime) SetDeleteError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteErr = err
}

// DeleteCalls 返回 Delete 接收的 ID 独立快照。
func (f *FakeRuntime) DeleteCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleteCalls...)
}

// Delete 记录 ID 并返回预先配置的错误。
func (f *FakeRuntime) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls = append(f.deleteCalls, id)
	return f.deleteErr
}

// SetListManagedResult 配置 ListManaged 返回的实际状态列表和错误。
func (f *FakeRuntime) SetListManagedResult(
	result []runtimeport.ActualSandbox,
	err error,
) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listManagedResult = append([]runtimeport.ActualSandbox(nil), result...)
	f.listManagedErr = err
}

// ListManagedCallCount 返回 ListManaged 的累计调用次数。
func (f *FakeRuntime) ListManagedCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listManagedCalls
}

// ListManaged 记录调用并返回预先配置的实际状态列表副本。
func (f *FakeRuntime) ListManaged(
	context.Context,
) ([]runtimeport.ActualSandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listManagedCalls++
	return append([]runtimeport.ActualSandbox(nil), f.listManagedResult...),
		f.listManagedErr
}

var _ runtimeport.Runtime = (*FakeRuntime)(nil)
