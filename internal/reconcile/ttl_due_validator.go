package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// TTLDueStore 是到期候选复核所需的最小持久化读取端口。
type TTLDueStore interface {
	// Get 重读当前权威 sandbox；不存在时返回 domain.ErrNotFound。
	Get(context.Context, string) (domain.Sandbox, error)
}

// TTLIndex 接受 validator 对当前有效租约的重排或移除请求。
type TTLIndex interface {
	// Upsert 保存 ID 最新的绝对 expiry；相同值允许 no-op。
	Upsert(TTLHeapEntry) bool
	// Remove 幂等移除不再需要 TTL 调度的 sandbox ID。
	Remove(string) bool
}

// ValidatedTTLDue 是已通过 Store 当前租约复核、可交给 expire coordinator 的输入。
type ValidatedTTLDue struct {
	// Sandbox 是复核时读取的完整当前 snapshot，后续 CAS 使用其中 revision。
	Sandbox domain.Sandbox
	// CheckedAt 是确认当前租约已到期的服务端 UTC 时刻。
	CheckedAt time.Time
}

// TTLDueValidator 拒绝续期后迟到的旧 timer，并重排尚未到期的当前租约。
type TTLDueValidator struct {
	store TTLDueStore
	index TTLIndex
	clock Clock
}

// NewTTLDueValidator 创建只读 Store、不会提交 expire intent 的到期复核器。
func NewTTLDueValidator(store TTLDueStore, index TTLIndex, clock Clock) *TTLDueValidator {
	return &TTLDueValidator{store: store, index: index, clock: clock}
}

// Validate 重读 Store 并确认 entry 的 expiry 仍是当前且确实已经到期。
//
// 返回 ok=false 表示 entry 已安全失效或被重排；普通 revision 变化不会影响
// 判断。Store 故障不制造内存中的权威结论，由周期 scanner 或上层 retry 兜底。
func (v *TTLDueValidator) Validate(ctx context.Context, entry TTLHeapEntry) (result ValidatedTTLDue, ok bool, err error) {
	if v == nil || v.store == nil || v.index == nil || v.clock == nil ||
		entry.SandboxID == "" || entry.ExpectedExpiresAt.IsZero() {
		return ValidatedTTLDue{}, false, fmt.Errorf("validate TTL due entry: %w", domain.ErrInvalid)
	}
	current, err := v.store.Get(ctx, entry.SandboxID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			v.index.Remove(entry.SandboxID)
			return ValidatedTTLDue{}, false, nil
		}
		return ValidatedTTLDue{}, false, fmt.Errorf("validate TTL due entry: read Store: %w", err)
	}
	if current.DesiredState == domain.DesiredTerminated || current.ObservedState == domain.StateTerminated {
		v.index.Remove(entry.SandboxID)
		return ValidatedTTLDue{}, false, nil
	}
	if current.ExpiresAt == nil {
		v.index.Remove(entry.SandboxID)
		return ValidatedTTLDue{}, false, fmt.Errorf("validate TTL due entry: missing expiry: %w", storeport.ErrCorrupt)
	}
	currentEntry := TTLHeapEntry{SandboxID: current.ID, ExpectedExpiresAt: current.ExpiresAt.UTC()}
	if !current.ExpiresAt.Equal(entry.ExpectedExpiresAt) {
		// timer 身份只绑定 expiry；renew 后旧 callback 不得借旧 revision 或旧时间
		// 提交动作，但必须确保新的租约仍留在 scheduler 中。
		v.index.Upsert(currentEntry)
		return ValidatedTTLDue{}, false, nil
	}
	now := v.clock.Now().UTC()
	if now.Before(currentEntry.ExpectedExpiresAt) {
		// timer 可能因 wall clock 调整或 reset race 提前 firing；重排同一租约，
		// 不把“timer 已触发”误当成“Store 租约已过期”。
		v.index.Upsert(currentEntry)
		return ValidatedTTLDue{}, false, nil
	}
	return ValidatedTTLDue{Sandbox: current, CheckedAt: now}, true, nil
}

var _ TTLDueStore = (storeport.Store)(nil)
