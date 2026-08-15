package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/domain"

	"go.yaml.in/yaml/v3"
)

// TestDefaultSnapshot 以快照方式钉死全部默认值,任何默认值变化都必须
// 显式更新本测试并同步示例配置与文档。
func TestDefaultSnapshot(t *testing.T) {
	want := Config{
		Server: ServerConfig{
			ListenAddress:   "127.0.0.1:8080",
			ShutdownTimeout: 10 * time.Second,
		},
		Data: DataConfig{
			Directory:  "/var/lib/minisandbox",
			SQLitePath: "/var/lib/minisandbox/sandboxd.db",
		},
		Runtime: RuntimeConfig{
			Type:                  "docker",
			DockerHost:            "unix:///var/run/docker.sock",
			DefaultImage:          "debian:bookworm-slim",
			RunnerSocketDirectory: "/var/lib/minisandbox/run",
			WorkspaceDirectory:    "/var/lib/minisandbox/workspaces",
			NetworkMode:           "none",
			WorkspacePersistent:   false,
			Platform: domain.Platform{
				OS:   "linux",
				Arch: "amd64",
			},
		},
		Limits: LimitsConfig{
			DefaultTTL:              30 * time.Minute,
			MinimumTTL:              time.Minute,
			MaximumTTL:              24 * time.Hour,
			MaxSandboxes:            100,
			MaxConcurrentCreates:    4,
			MaxConcurrentImagePulls: 2,
			MaxConcurrentDeletes:    4,
			DefaultResources: domain.ResourceLimits{
				CPUQuotaMillis: 500,
				MemoryMiB:      512,
				PIDs:           128,
			},
			MaxResources: domain.ResourceBounds{
				MaxCPUQuotaMillis: 4000,
				MaxMemoryMiB:      8192,
				MaxPIDs:           1024,
			},
		},
		Runner: RunnerConfig{
			ExecutionUID:            1000,
			ExecutionGID:            1000,
			DefaultCWD:              "/workspace",
			DefaultTimeout:          10 * time.Minute,
			MaxTimeout:              time.Hour,
			TerminationGrace:        2 * time.Second,
			MaxConcurrentExecutions: 8,
			MaxRequestBytes:         1_048_576,
			MaxOutputBytes:          10_485_760,
			MaxEnvVars:              128,
			MaxEnvKeyBytes:          128,
			MaxEnvValueBytes:        8_192,
			MaxEnvTotalBytes:        65_536,
			MaxLogPageEvents:        256,
			MaxLogPageBytes:         1_048_576,
			CompletedRetention:      time.Hour,
			MaxRetainedExecutions:   100,
			SSEWriteTimeout:         15 * time.Second,
		},
		Security: SecurityConfig{
			RunnerMasterKeyFile: "/etc/minisandbox/runner-master-key",
			AllowOutbound:       false,
		},
		Egress: EgressConfig{
			Image:           "",
			ProtocolVersion: 1,
			ReadyTimeout:    30 * time.Second,
			DeniedCIDRs:     nil,
			AnchorUID:       65532,
			AnchorGID:       65532,
			Limits: domain.ResourceLimits{
				CPUQuotaMillis: 100,
				MemoryMiB:      64,
				PIDs:           16,
			},
		},
		Reconcile: ReconcileConfig{
			Interval:                 10 * time.Second,
			Jitter:                   2 * time.Second,
			Timeout:                  2 * time.Minute,
			PageSize:                 100,
			MaxConcurrent:            8,
			RetryMin:                 time.Second,
			RetryMax:                 time.Minute,
			RunningCheckInterval:     30 * time.Second,
			RunnerUnhealthyThreshold: 3,
			DockerFreshness:          30 * time.Second,
			RunnerReadyTimeout:       30 * time.Second,
			DeletionTimeout:          30 * time.Second,
		},
		Idempotency: IdempotencyConfig{
			MaxKeyBytes:       128,
			TerminalRetention: 24 * time.Hour,
			GCInterval:        10 * time.Minute,
		},
		Recovery: RecoveryConfig{
			ImportTrustedOrphans:     true,
			RecordAmbiguousAnomalies: true,
		},
		Admin: AdminConfig{
			Enabled:   false,
			TokenFile: "",
		},
		Files: FilesConfig{
			Enabled:          true,
			MaxUploadBytes:   33_554_432,
			MaxDownloadBytes: 67_108_864,
		},
		PTY: PTYConfig{
			Enabled:               true,
			MaxConcurrentSessions: 2,
			DefaultTimeout:        time.Hour,
		},
		PortProxy: PortProxyConfig{
			Enabled: true,
			MinPort: 1024,
			MaxPort: 65535,
		},
	}

	if got := Default(); !reflect.DeepEqual(got, want) {
		t.Fatalf("default config snapshot mismatch:\ngot  %+v\nwant %+v", got, want)
	}
}

// TestAdminTokenFileExcludedFromConfigDumps 验证普通结构化配置输出不会泄露
// admin secret file 路径；实际启动加载器仍可直接读取强类型字段。
func TestAdminTokenFileExcludedFromConfigDumps(t *testing.T) {
	cfg := Default()
	cfg.Admin.TokenFile = "/run/secrets/admin-token-canary"

	jsonDump, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal JSON config dump: %v", err)
	}
	yamlDump, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal YAML config dump: %v", err)
	}
	for name, dump := range map[string][]byte{"json": jsonDump, "yaml": yamlDump} {
		text := string(dump)
		if strings.Contains(text, "admin-token-canary") || strings.Contains(text, "TokenFile") || strings.Contains(text, "token_file") {
			t.Fatalf("%s config dump leaked admin token file reference: %s", name, dump)
		}
	}
}

// TestDefaultSandboxSpecComplete 验证默认配置能生成完整且自洽的 resolved spec。
func TestDefaultSandboxSpecComplete(t *testing.T) {
	cfg := Default()
	spec := cfg.DefaultSandboxSpec()

	want := domain.SandboxSpec{
		Image: "debian:bookworm-slim",
		Resources: domain.ResourceLimits{
			CPUQuotaMillis: 500,
			MemoryMiB:      512,
			PIDs:           128,
		},
		Workspace: domain.WorkspaceSpec{
			MountPath:  "/workspace",
			Persistent: false,
		},
		Network: domain.NetworkSpec{
			Outbound: false,
		},
		Platform: domain.Platform{
			OS:   "linux",
			Arch: "amd64",
		},
	}
	if !reflect.DeepEqual(spec, want) {
		t.Fatalf("default resolved spec mismatch:\ngot  %+v\nwant %+v", spec, want)
	}

	// 默认 spec 必须能通过领域校验,证明默认值之间没有互相矛盾。
	if err := spec.Validate(cfg.Limits.MaxResources); err != nil {
		t.Fatalf("default resolved spec fails validation: %v", err)
	}
}

// TestDefaultSandboxSpecDerivesFromConfig 验证 spec 派生自配置字段,
// 而不是方法内部的硬编码值。
func TestDefaultSandboxSpecDerivesFromConfig(t *testing.T) {
	cfg := Default()
	cfg.Runtime.DefaultImage = "alpine:3.22"
	cfg.Limits.DefaultResources.MemoryMiB = 1024
	cfg.Runtime.NetworkMode = "bridge"

	spec := cfg.DefaultSandboxSpec()
	if got, want := spec.Image, "alpine:3.22"; got != want {
		t.Fatalf("image not derived from config: got %s, want %s", got, want)
	}
	if got, want := spec.Resources.MemoryMiB, int64(1024); got != want {
		t.Fatalf("memory not derived from config: got %d, want %d", got, want)
	}
	// 非 none 网络模式映射为 Outbound=true,由领域校验兜底拒绝,
	// 而不是在映射层被静默改写为安全值。
	if !spec.Network.Outbound {
		t.Fatal("non-none network mode must map to outbound=true")
	}
}
