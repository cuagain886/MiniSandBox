package logging

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// TestOperationContextNestedOverrideAndEmptyValues 验证 child 只覆盖显式字段且零值安全省略。
func TestOperationContextNestedOverrideAndEmptyValues(t *testing.T) {
	base := context.Background()
	component, _ := NewSafeValue("api")
	request := mustSafeID(t, IDKindRequest, "req-parent")
	sandbox := mustSafeID(t, IDKindSandbox, "sandbox-parent")
	base = WithComponent(WithRequestID(base, request), component)
	child := WithSandboxID(base, sandbox)
	override := WithRequestID(child, mustSafeID(t, IDKindRequest, "req-child"))

	if got := attrMap(ContextAttrs(base)); !reflect.DeepEqual(got, map[string]any{"component": "api", "request_id": "req-parent"}) {
		t.Fatalf("base fields: %#v", got)
	}
	if got := attrMap(ContextAttrs(child)); got["request_id"] != "req-parent" || got["sandbox_id"] != "sandbox-parent" {
		t.Fatalf("child fields: %#v", got)
	}
	if got := attrMap(ContextAttrs(override)); got["request_id"] != "req-child" || attrMap(ContextAttrs(child))["request_id"] != "req-parent" {
		t.Fatalf("override mutated parent: %#v", got)
	}
	if got := ContextAttrs(WithExecutionID(child, SafeID{})); len(got) != 3 {
		t.Fatalf("zero ID was not omitted: %#v", got)
	}
}

// TestOperationContextConcurrentIsolation 验证并发请求不会串用 request/sandbox ID。
func TestOperationContextConcurrentIsolation(t *testing.T) {
	const workers = 32
	var wait sync.WaitGroup
	failures := make(chan string, workers)
	for index := range workers {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			requestValue := fmt.Sprintf("req-%02d", index)
			sandboxValue := fmt.Sprintf("sandbox-%02d", index)
			requestID, requestErr := NewSafeID(IDKindRequest, requestValue)
			sandboxID, sandboxErr := NewSafeID(IDKindSandbox, sandboxValue)
			if requestErr != nil || sandboxErr != nil {
				failures <- requestValue
				return
			}
			ctx := WithRequestID(context.Background(), requestID)
			ctx = WithSandboxID(ctx, sandboxID)
			got := attrMap(ContextAttrs(ctx))
			if got["request_id"] != requestValue || got["sandbox_id"] != sandboxValue {
				failures <- requestValue
			}
		}(index)
	}
	wait.Wait()
	close(failures)
	for failure := range failures {
		t.Fatalf("context fields crossed at %s", failure)
	}
}

// TestOperationContextPreservesCancellationAndIgnoresForeignValues 验证 context 取消继续传播且任意 value 不会进入日志。
func TestOperationContextPreservesCancellationAndIgnoresForeignValues(t *testing.T) {
	type foreignKey struct{}
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), foreignKey{}, "secret"))
	child := WithRequestID(parent, mustSafeID(t, IDKindRequest, "req-cancel"))
	cancel()
	if child.Err() != context.Canceled {
		t.Fatalf("cancellation lost: %v", child.Err())
	}
	if got := attrMap(ContextAttrs(child)); len(got) != 1 || got["request_id"] != "req-cancel" {
		t.Fatalf("foreign context serialized: %#v", got)
	}
}

func attrMap(attrs []Attr) map[string]any {
	result := make(map[string]any, len(attrs))
	for _, attr := range attrs {
		result[attr.value.Key] = attr.value.Value.Any()
	}
	return result
}
