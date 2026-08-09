package docker

import (
	"context"
	"testing"

	runtimeport "minisandbox/internal/runtime"
)

type recordingBootstrap struct{ calls []string }

// Stage 记录 runtime 在容器创建前重建受信材料的目录与 sandbox ID。
func (s *recordingBootstrap) Stage(directory, sandboxID string) error {
	s.calls = append(s.calls, directory+":"+sandboxID)
	return nil
}

// TestEnsureStagesRunnerBootstrap 验证 stopped/missing 创建路径会准备 runner 配置和 token。
func TestEnsureStagesRunnerBootstrap(t *testing.T) {
	harness := newEnsureHarness(t, runtimeport.ActualMissing)
	harness.volumeMissing = true
	runtime := newEnsureRuntime(t, harness.engine)
	stager := &recordingBootstrap{}
	runtime.bootstrap = stager
	if _, err := runtime.Ensure(context.Background(), testDockerSandbox()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(stager.calls) != 1 {
		t.Fatalf("bootstrap calls: %v", stager.calls)
	}
}
