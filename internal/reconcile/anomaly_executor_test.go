package reconcile

import (
	"context"
	"reflect"
	"testing"
	"time"

	storeport "minisandbox/internal/store"
)

// TestRecoveryAnomalyExecutorRecordsAmbiguousCasesWithoutMutationPort 验证未知 schema、缺 label、partial 和伪造资源只进入诊断端口。
func TestRecoveryAnomalyExecutorRecordsAmbiguousCasesWithoutMutationPort(t *testing.T) {
	repository := &anomalyRecordingRepository{}
	executor, err := NewRecoveryAnomalyExecutor(repository)
	if err != nil {
		t.Fatal(err)
	}
	// 安全边界由构造依赖证明：执行器唯一字段是 anomaly repository，未来若加入 Runtime 会使本测试失败。
	typeOfExecutor := reflect.TypeOf(*executor)
	if typeOfExecutor.NumField() != 1 || typeOfExecutor.Field(0).Name != "repository" {
		t.Fatalf("anomaly executor gained mutation capability: %v", typeOfExecutor)
	}

	now := time.Now().UTC()
	cases := []ActualResourceSnapshot{
		{SandboxID: "unknown-schema", Anomalies: []ActualAnomaly{{Code: ActualAnomalyResourceDamaged, Resource: "main", Detail: "SCHEMA_UNSUPPORTED"}}},
		{SandboxID: "only-volume"},
		{SandboxID: "only-directory"},
	}
	for _, actual := range cases {
		plan := PlanRecovery(nil, &actual)
		if plan.Action != RecoveryActionRecordAnomaly {
			t.Fatalf("plan %s: %#v", actual.SandboxID, plan)
		}
		if err := executor.Execute(context.Background(), plan, actual, now); err != nil {
			t.Fatalf("execute %s: %v", actual.SandboxID, err)
		}
	}
	if err := executor.RecordUnscoped(context.Background(), []ActualAnomaly{
		{Code: ActualAnomalyResourceDamaged, Resource: "container", Detail: "LABELS_MISSING"},
		{Code: ActualAnomalyResourceDamaged, Resource: "container", Detail: "MANAGED_NAME_FORGED"},
	}, now); err != nil {
		t.Fatal(err)
	}
	if len(repository.observations) != 5 {
		t.Fatalf("not all ambiguous facts recorded: %#v", repository.observations)
	}
	for _, observation := range repository.observations {
		if observation.Classification != storeport.RuntimeAnomalyIncompleteBundle &&
			observation.Classification != storeport.RuntimeAnomalyUnknownSchema {
			t.Fatalf("unsafe classification: %#v", observation)
		}
	}
}

// TestRecoveryAnomalyExecutorRejectsNonAnomalyPlan 验证只读异常执行器不能被复用来执行导入或唤醒计划。
func TestRecoveryAnomalyExecutorRejectsNonAnomalyPlan(t *testing.T) {
	executor, err := NewRecoveryAnomalyExecutor(&anomalyRecordingRepository{})
	if err != nil {
		t.Fatal(err)
	}
	actual := ActualResourceSnapshot{SandboxID: "trusted"}
	for _, action := range []RecoveryAction{RecoveryActionNoOp, RecoveryActionWake, RecoveryActionImport, RecoveryActionRepairMetadata} {
		if err := executor.Execute(context.Background(), RecoveryPlan{SandboxID: actual.SandboxID, Action: action}, actual, time.Now()); err == nil {
			t.Fatalf("accepted action %s", action)
		}
	}
}
