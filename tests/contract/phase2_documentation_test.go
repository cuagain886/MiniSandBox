package contract

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"minisandbox/internal/config"
)

// TestExampleConfigMatchesDocumentedDefaults 验证示例配置可以加载并通过校验，
// 且关键默认值与根 README、sdk/go 文档中宣传的行为保持一致。
//
// 历史上本文件还校验 docs/ 下的运维指南；该指南已随文档目录重组迁出仓库
// 跟踪范围，文档内示例不再属于仓库交付物，相关断言随之移除。
func TestExampleConfigMatchesDocumentedDefaults(t *testing.T) {
	configPath := filepath.Join(documentationRepositoryRoot(t), "configs", "sandboxd.example.yaml")
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load documented example config: %v", err)
	}
	if err := loaded.Validate(); err != nil {
		t.Fatalf("validate documented example config: %v", err)
	}
	if loaded.Server.ListenAddress != "127.0.0.1:8080" || loaded.Runner.DefaultCWD != "/workspace" ||
		loaded.Runner.DefaultTimeout != 10*time.Minute || loaded.Runner.MaxTimeout != time.Hour ||
		loaded.Runner.TerminationGrace != 2*time.Second || loaded.Runner.MaxConcurrentExecutions != 8 ||
		loaded.Runner.MaxOutputBytes != 10_485_760 || loaded.Egress.ReadyTimeout != 30*time.Second || loaded.Security.AllowOutbound {
		t.Fatalf("example configuration defaults drifted: %+v", loaded)
	}
}

func documentationRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate documentation contract test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}
