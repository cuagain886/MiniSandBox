package reconcile

import "testing"

// TestDecideRetryCoversOperationsAndClasses 验证每类操作、错误和唯一 action 映射。
func TestDecideRetryCoversOperationsAndClasses(t *testing.T) {
	operations := []RetryOperation{
		RetryOperationCreate, RetryOperationStart, RetryOperationHealth,
		RetryOperationDelete, RetryOperationExpire, RetryOperationCleanup, RetryOperationRecover,
	}
	classes := []struct {
		class   RetryErrorClass
		action  RetryAction
		account bool
	}{
		{RetryErrorShutdown, RetryActionDoNotRetry, false},
		{RetryErrorConflict, RetryActionImmediateReread, false},
		{RetryErrorTransient, RetryActionRetryAt, true},
		{RetryErrorPermanent, RetryActionDoNotRetry, true},
	}
	for _, operation := range operations {
		for _, class := range classes {
			decision, err := DecideRetry(RetryPolicyInput{Operation: operation, ErrorClass: class.class, Attempt: 42})
			if err != nil || decision.Action != class.action || decision.AccountFailure != class.account {
				t.Fatalf("operation=%s class=%s decision=%#v err=%v", operation, class.class, decision, err)
			}
			wantConverge := operation == RetryOperationDelete || operation == RetryOperationExpire || operation == RetryOperationCleanup
			if decision.MustConverge != wantConverge {
				t.Fatalf("operation=%s must-converge=%t", operation, decision.MustConverge)
			}
		}
	}
}

// TestDecideRetryAlreadyAbsentOnlyForConvergentOperations 验证 not-found 成功语义不扩散到 create/health。
func TestDecideRetryAlreadyAbsentOnlyForConvergentOperations(t *testing.T) {
	for _, operation := range []RetryOperation{RetryOperationDelete, RetryOperationExpire, RetryOperationCleanup} {
		decision, err := DecideRetry(RetryPolicyInput{Operation: operation, ErrorClass: RetryErrorAlreadyAbsent})
		if err != nil || decision.Action != RetryActionDoNotRetry || decision.AccountFailure || !decision.MustConverge {
			t.Fatalf("operation=%s decision=%#v err=%v", operation, decision, err)
		}
	}
	for _, operation := range []RetryOperation{RetryOperationCreate, RetryOperationStart, RetryOperationHealth, RetryOperationRecover} {
		if _, err := DecideRetry(RetryPolicyInput{Operation: operation, ErrorClass: RetryErrorAlreadyAbsent}); err == nil {
			t.Fatalf("operation=%s accepted already-absent", operation)
		}
	}
}

// TestDecideRetryRejectsUnknownValues 验证零值和未来未知枚举 fail closed。
func TestDecideRetryRejectsUnknownValues(t *testing.T) {
	inputs := []RetryPolicyInput{
		{},
		{Operation: "future", ErrorClass: RetryErrorTransient},
		{Operation: RetryOperationCreate, ErrorClass: "future"},
	}
	for _, input := range inputs {
		if decision, err := DecideRetry(input); err == nil || decision != (RetryDecision{}) {
			t.Fatalf("input=%#v decision=%#v err=%v", input, decision, err)
		}
	}
}
