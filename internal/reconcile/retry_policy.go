package reconcile

import "errors"

// RetryOperation 标识发生失败的稳定 reconcile 操作类别。
type RetryOperation string

const (
	// RetryOperationCreate 表示创建 runtime 资源。
	RetryOperationCreate RetryOperation = "create"
	// RetryOperationStart 表示启动并等待 runner 就绪。
	RetryOperationStart RetryOperation = "start"
	// RetryOperationHealth 表示 Running 健康探测。
	RetryOperationHealth RetryOperation = "health"
	// RetryOperationDelete 表示响应显式删除意图，必须最终收敛。
	RetryOperationDelete RetryOperation = "delete"
	// RetryOperationExpire 表示响应 TTL 到期意图，必须最终收敛。
	RetryOperationExpire RetryOperation = "expire"
	// RetryOperationCleanup 表示清理部分创建或异常资源，必须最终收敛。
	RetryOperationCleanup RetryOperation = "cleanup"
	// RetryOperationRecover 表示启动恢复与可信 orphan 对账。
	RetryOperationRecover RetryOperation = "recover"
)

// RetryErrorClass 是不包含 cause 文本的稳定失败类别。
type RetryErrorClass string

const (
	// RetryErrorShutdown 表示 server lifetime context 已取消，不计失败次数。
	RetryErrorShutdown RetryErrorClass = "shutdown"
	// RetryErrorConflict 表示 Store CAS snapshot 过期，应立即重读。
	RetryErrorConflict RetryErrorClass = "conflict"
	// RetryErrorTransient 表示基础设施暂时不可用，可进入 backoff。
	RetryErrorTransient RetryErrorClass = "transient"
	// RetryErrorPermanent 表示规格、安全或协议错误，自动重试没有意义。
	RetryErrorPermanent RetryErrorClass = "permanent"
	// RetryErrorAlreadyAbsent 表示删除类操作的目标已经不存在，视为满足目标。
	RetryErrorAlreadyAbsent RetryErrorClass = "already_absent"
)

// RetryAction 是后续控制流允许采用的唯一动作。
type RetryAction string

const (
	// RetryActionRetryAt 要求后续 backoff 模块计算并持久化下一次时间。
	RetryActionRetryAt RetryAction = "retry_at"
	// RetryActionDoNotRetry 表示当前失败不安排自动重试。
	RetryActionDoNotRetry RetryAction = "do_not_retry"
	// RetryActionImmediateReread 表示不计失败并立即从 Store 重读最新 revision。
	RetryActionImmediateReread RetryAction = "immediate_reread"
)

// RetryPolicyInput 是纯策略判断的完整输入。
type RetryPolicyInput struct {
	// Operation 是当前 reconcile 操作。
	Operation RetryOperation
	// ErrorClass 是 typed classifier 产生的稳定类别。
	ErrorClass RetryErrorClass
	// Attempt 是 Store 已持久化的失败次数，零表示首次失败。
	Attempt uint32
}

// RetryDecision 描述策略动作及记账/收敛属性。
type RetryDecision struct {
	// Action 是 retry-at、do-not-retry 或 immediate-reread 之一。
	Action RetryAction
	// AccountFailure 表示后续成功持久化时是否增加 attempt。
	AccountFailure bool
	// MustConverge 标记 delete/expire/cleanup 不能被普通失败遗忘。
	MustConverge bool
}

// DecideRetry 验证输入并返回不含时间计算和底层错误的策略结果。
func DecideRetry(input RetryPolicyInput) (RetryDecision, error) {
	if !validRetryOperation(input.Operation) || !validRetryErrorClass(input.ErrorClass) {
		return RetryDecision{}, errors.New("invalid retry policy input")
	}
	mustConverge := input.Operation == RetryOperationDelete ||
		input.Operation == RetryOperationExpire || input.Operation == RetryOperationCleanup
	decision := RetryDecision{MustConverge: mustConverge}
	switch input.ErrorClass {
	case RetryErrorShutdown:
		decision.Action = RetryActionDoNotRetry
	case RetryErrorConflict:
		decision.Action = RetryActionImmediateReread
	case RetryErrorTransient:
		decision.Action = RetryActionRetryAt
		decision.AccountFailure = true
	case RetryErrorPermanent:
		decision.Action = RetryActionDoNotRetry
		decision.AccountFailure = true
	case RetryErrorAlreadyAbsent:
		if !mustConverge {
			return RetryDecision{}, errors.New("already-absent is only valid for convergent operations")
		}
		decision.Action = RetryActionDoNotRetry
	}
	return decision, nil
}

func validRetryOperation(operation RetryOperation) bool {
	switch operation {
	case RetryOperationCreate, RetryOperationStart, RetryOperationHealth,
		RetryOperationDelete, RetryOperationExpire, RetryOperationCleanup, RetryOperationRecover:
		return true
	default:
		return false
	}
}

func validRetryErrorClass(class RetryErrorClass) bool {
	switch class {
	case RetryErrorShutdown, RetryErrorConflict, RetryErrorTransient, RetryErrorPermanent, RetryErrorAlreadyAbsent:
		return true
	default:
		return false
	}
}
