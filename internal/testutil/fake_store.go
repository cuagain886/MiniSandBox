// Package testutil 提供 application 和 reconciler 单元测试使用的确定性替身。
//
// 本模块只记录端口调用并返回测试预先配置的结果，不模拟 SQLite、Docker 或
// 生命周期状态机，也不得进入生产依赖装配。
package testutil

import (
	"context"
	"sync"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// DesiredUpdateCall 记录一次 Store.UpdateDesired 调用参数。
type DesiredUpdateCall struct {
	// ID 是被更新的 sandbox 标识。
	ID string
	// Desired 是调用方提交的期望状态。
	Desired domain.DesiredState
	// ExpectedRevision 是调用方提交的 CAS revision。
	ExpectedRevision uint64
}

// FakeStore 是线程安全、可注入结果并记录调用的 Store 测试替身。
type FakeStore struct {
	mu sync.Mutex

	createErr                 error
	createCalls               []domain.Sandbox
	createIdempotentResult    storeport.IdempotentCreateResult
	createIdempotentErr       error
	createIdempotentCalls     []storeport.IdempotentCreateRequest
	createNonIdempotentResult domain.Sandbox
	createNonIdempotentErr    error
	createNonIdempotentCalls  []storeport.NonIdempotentCreateRequest

	getResult domain.Sandbox
	getErr    error
	getCalls  []string

	updateDesiredResult domain.Sandbox
	updateDesiredErr    error
	updateDesiredCalls  []DesiredUpdateCall

	updateObservedResult domain.Sandbox
	updateObservedErr    error
	updateObservedCalls  []storeport.ObservedUpdate
	renewResult          domain.Sandbox
	renewErr             error
	renewCalls           []storeport.RenewUpdate
	expireIntentResult   domain.Sandbox
	expireIntentErr      error
	expireIntentCalls    []storeport.ExpireIntentUpdate
	scheduleRetryResult  domain.Sandbox
	scheduleRetryErr     error
	scheduleRetryCalls   []storeport.RetryUpdate
	resetRetryResult     domain.Sandbox
	resetRetryErr        error
	resetRetryCalls      []storeport.RetryResetUpdate
	healthResult         domain.Sandbox
	healthErr            error
	healthCalls          []storeport.HealthResultUpdate

	listCandidatesResult []domain.Sandbox
	listCandidatesErr    error
	listCandidatesCalls  []storeport.ReconcileCandidateQuery

	listAllResult []domain.Sandbox
	listAllErr    error
	listAllCalls  int
}

// NewFakeStore 创建所有方法默认成功并返回零值结果的 Store 替身。
func NewFakeStore() *FakeStore {
	return &FakeStore{}
}

// SetCreateError 配置 Create 返回的错误；传入 nil 恢复成功。
func (f *FakeStore) SetCreateError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createErr = err
}

// CreateCalls 返回 Create 调用参数的独立快照。
func (f *FakeStore) CreateCalls() []domain.Sandbox {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Sandbox(nil), f.createCalls...)
}

// Create 记录 sandbox 并返回预先配置的错误，不维护内存数据库。
func (f *FakeStore) Create(_ context.Context, sandbox domain.Sandbox) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls = append(f.createCalls, sandbox)
	return f.createErr
}

// SetCreateNonIdempotentResult 配置无 key 原子创建结果。
func (f *FakeStore) SetCreateNonIdempotentResult(result domain.Sandbox, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createNonIdempotentResult, f.createNonIdempotentErr = result, err
}

// CreateNonIdempotentCalls 返回无 key 创建参数快照。
func (f *FakeStore) CreateNonIdempotentCalls() []storeport.NonIdempotentCreateRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storeport.NonIdempotentCreateRequest(nil), f.createNonIdempotentCalls...)
}

// CreateNonIdempotent 记录无 key 创建并返回预设结果。
func (f *FakeStore) CreateNonIdempotent(_ context.Context, request storeport.NonIdempotentCreateRequest) (domain.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createNonIdempotentCalls = append(f.createNonIdempotentCalls, request)
	return f.createNonIdempotentResult, f.createNonIdempotentErr
}

// SetCreateIdempotentResult 配置 CreateIdempotent 返回的结果和错误。
func (f *FakeStore) SetCreateIdempotentResult(result storeport.IdempotentCreateResult, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createIdempotentResult, f.createIdempotentErr = result, err
}

// CreateIdempotentCalls 返回原子创建请求的独立深拷贝快照。
func (f *FakeStore) CreateIdempotentCalls() []storeport.IdempotentCreateRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := append([]storeport.IdempotentCreateRequest(nil), f.createIdempotentCalls...)
	for index := range result {
		result[index].Response.Body = append([]byte(nil), result[index].Response.Body...)
	}
	return result
}

// CreateIdempotent 记录请求并返回预先配置的结果副本。
func (f *FakeStore) CreateIdempotent(_ context.Context, request storeport.IdempotentCreateRequest) (storeport.IdempotentCreateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	request.Response.Body = append([]byte(nil), request.Response.Body...)
	f.createIdempotentCalls = append(f.createIdempotentCalls, request)
	result := f.createIdempotentResult
	result.Response.Body = append([]byte(nil), result.Response.Body...)
	return result, f.createIdempotentErr
}

// SetGetResult 配置 Get 返回的领域对象和错误。
func (f *FakeStore) SetGetResult(result domain.Sandbox, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getResult = result
	f.getErr = err
}

// GetCalls 返回 Get 接收的 ID 独立快照。
func (f *FakeStore) GetCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.getCalls...)
}

// Get 记录 ID 并返回预先配置的结果。
func (f *FakeStore) Get(_ context.Context, id string) (domain.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls = append(f.getCalls, id)
	return f.getResult, f.getErr
}

// SetUpdateDesiredResult 配置 UpdateDesired 返回的领域对象和错误。
func (f *FakeStore) SetUpdateDesiredResult(result domain.Sandbox, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateDesiredResult = result
	f.updateDesiredErr = err
}

// UpdateDesiredCalls 返回 UpdateDesired 调用参数的独立快照。
func (f *FakeStore) UpdateDesiredCalls() []DesiredUpdateCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]DesiredUpdateCall(nil), f.updateDesiredCalls...)
}

// UpdateDesired 记录 CAS 参数并返回预先配置的结果。
func (f *FakeStore) UpdateDesired(
	_ context.Context,
	id string,
	desired domain.DesiredState,
	expectedRevision uint64,
) (domain.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateDesiredCalls = append(f.updateDesiredCalls, DesiredUpdateCall{
		ID:               id,
		Desired:          desired,
		ExpectedRevision: expectedRevision,
	})
	return f.updateDesiredResult, f.updateDesiredErr
}

// SetUpdateObservedResult 配置 UpdateObserved 返回的领域对象和错误。
func (f *FakeStore) SetUpdateObservedResult(result domain.Sandbox, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateObservedResult = result
	f.updateObservedErr = err
}

// UpdateObservedCalls 返回 UpdateObserved 调用参数的独立快照。
func (f *FakeStore) UpdateObservedCalls() []storeport.ObservedUpdate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storeport.ObservedUpdate(nil), f.updateObservedCalls...)
}

// UpdateObserved 记录观测状态 CAS 参数并返回预先配置的结果。
func (f *FakeStore) UpdateObserved(
	_ context.Context,
	update storeport.ObservedUpdate,
) (domain.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateObservedCalls = append(f.updateObservedCalls, update)
	return f.updateObservedResult, f.updateObservedErr
}

// SetRenewResult 配置 Renew 返回的领域对象和错误。
func (f *FakeStore) SetRenewResult(result domain.Sandbox, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewResult, f.renewErr = result, err
}

// RenewCalls 返回 Renew 调用参数的独立快照。
func (f *FakeStore) RenewCalls() []storeport.RenewUpdate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storeport.RenewUpdate(nil), f.renewCalls...)
}

// Renew 记录租约 CAS 参数并返回预先配置的结果。
func (f *FakeStore) Renew(_ context.Context, update storeport.RenewUpdate) (domain.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewCalls = append(f.renewCalls, update)
	return f.renewResult, f.renewErr
}

// SetExpireIntentResult 配置 ExpireIntent 返回的领域对象和错误。
func (f *FakeStore) SetExpireIntentResult(result domain.Sandbox, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expireIntentResult, f.expireIntentErr = result, err
}

// ExpireIntentCalls 返回 ExpireIntent 调用参数的独立快照。
func (f *FakeStore) ExpireIntentCalls() []storeport.ExpireIntentUpdate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storeport.ExpireIntentUpdate(nil), f.expireIntentCalls...)
}

// ExpireIntent 记录到期意图参数并返回预先配置的结果。
func (f *FakeStore) ExpireIntent(_ context.Context, update storeport.ExpireIntentUpdate) (domain.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expireIntentCalls = append(f.expireIntentCalls, update)
	return f.expireIntentResult, f.expireIntentErr
}

// SetScheduleRetryResult 配置 ScheduleRetry 返回的领域对象和错误。
func (f *FakeStore) SetScheduleRetryResult(result domain.Sandbox, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scheduleRetryResult, f.scheduleRetryErr = result, err
}

// ScheduleRetryCalls 返回 ScheduleRetry 调用参数的独立快照。
func (f *FakeStore) ScheduleRetryCalls() []storeport.RetryUpdate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storeport.RetryUpdate(nil), f.scheduleRetryCalls...)
}

// ScheduleRetry 记录失败调度参数并返回预先配置的结果。
func (f *FakeStore) ScheduleRetry(_ context.Context, update storeport.RetryUpdate) (domain.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scheduleRetryCalls = append(f.scheduleRetryCalls, update)
	return f.scheduleRetryResult, f.scheduleRetryErr
}

// SetResetRetryResult 配置 ResetRetry 返回的领域对象和错误。
func (f *FakeStore) SetResetRetryResult(result domain.Sandbox, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetRetryResult, f.resetRetryErr = result, err
}

// ResetRetryCalls 返回 ResetRetry 调用参数的独立快照。
func (f *FakeStore) ResetRetryCalls() []storeport.RetryResetUpdate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storeport.RetryResetUpdate(nil), f.resetRetryCalls...)
}

// ResetRetry 记录成功收敛参数并返回预先配置的结果。
func (f *FakeStore) ResetRetry(_ context.Context, update storeport.RetryResetUpdate) (domain.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetRetryCalls = append(f.resetRetryCalls, update)
	return f.resetRetryResult, f.resetRetryErr
}

// SetHealthResult 配置 RecordHealthResult 返回的领域对象和错误。
func (f *FakeStore) SetHealthResult(result domain.Sandbox, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.healthResult, f.healthErr = result, err
}

// HealthResultCalls 返回 RecordHealthResult 调用参数的独立快照。
func (f *FakeStore) HealthResultCalls() []storeport.HealthResultUpdate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storeport.HealthResultUpdate(nil), f.healthCalls...)
}

// RecordHealthResult 记录 probe 结果并返回预先配置的结果。
func (f *FakeStore) RecordHealthResult(_ context.Context, update storeport.HealthResultUpdate) (domain.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.healthCalls = append(f.healthCalls, update)
	return f.healthResult, f.healthErr
}

// SetListReconcileCandidatesResult 配置候选查询返回的记录和错误。
func (f *FakeStore) SetListReconcileCandidatesResult(
	result []domain.Sandbox,
	err error,
) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCandidatesResult = append([]domain.Sandbox(nil), result...)
	f.listCandidatesErr = err
}

// ListReconcileCandidatesCalls 返回每次候选查询参数的独立快照。
func (f *FakeStore) ListReconcileCandidatesCalls() []storeport.ReconcileCandidateQuery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storeport.ReconcileCandidateQuery(nil), f.listCandidatesCalls...)
}

// ListReconcileCandidates 记录查询边界并返回预先配置的结果副本。
func (f *FakeStore) ListReconcileCandidates(
	_ context.Context,
	query storeport.ReconcileCandidateQuery,
) ([]domain.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCandidatesCalls = append(f.listCandidatesCalls, query)
	return append([]domain.Sandbox(nil), f.listCandidatesResult...),
		f.listCandidatesErr
}

// SetListAllResult 配置全量查询返回的记录和错误。
func (f *FakeStore) SetListAllResult(result []domain.Sandbox, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listAllResult = append([]domain.Sandbox(nil), result...)
	f.listAllErr = err
}

// ListAllCallCount 返回 ListAll 的累计调用次数。
func (f *FakeStore) ListAllCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listAllCalls
}

// ListAll 记录调用并返回预先配置的结果副本。
func (f *FakeStore) ListAll(context.Context) ([]domain.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listAllCalls++
	return append([]domain.Sandbox(nil), f.listAllResult...), f.listAllErr
}

var _ storeport.Store = (*FakeStore)(nil)
