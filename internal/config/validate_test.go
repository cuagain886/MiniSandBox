package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/domain"
)

// TestValidateRejections 逐条验证安全边界的拒绝规则。
func TestValidateRejections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		field  string
	}{
		{
			name:   "listen without port",
			mutate: func(c *Config) { c.Server.ListenAddress = "127.0.0.1" },
			field:  "server.listen_address",
		},
		{
			name:   "listen port out of range",
			mutate: func(c *Config) { c.Server.ListenAddress = "127.0.0.1:0" },
			field:  "server.listen_address",
		},
		{
			name:   "listen non-loopback ip",
			mutate: func(c *Config) { c.Server.ListenAddress = "0.0.0.0:8080" },
			field:  "server.listen_address",
		},
		{
			// 空 host 在 net.Listen 中表示监听所有接口,必须拒绝。
			name:   "listen empty host",
			mutate: func(c *Config) { c.Server.ListenAddress = ":8080" },
			field:  "server.listen_address",
		},
		{
			name:   "listen non-loopback host",
			mutate: func(c *Config) { c.Server.ListenAddress = "example.com:8080" },
			field:  "server.listen_address",
		},
		{
			name:   "shutdown timeout not positive",
			mutate: func(c *Config) { c.Server.ShutdownTimeout = 0 },
			field:  "server.shutdown_timeout",
		},
		{
			name:   "data directory empty",
			mutate: func(c *Config) { c.Data.Directory = "" },
			field:  "data.directory",
		},
		{
			name:   "data directory relative",
			mutate: func(c *Config) { c.Data.Directory = "var/lib/minisandbox" },
			field:  "data.directory",
		},
		{
			name:   "sqlite path relative",
			mutate: func(c *Config) { c.Data.SQLitePath = "state.db" },
			field:  "data.sqlite_path",
		},
		{
			name:   "runtime type not docker",
			mutate: func(c *Config) { c.Runtime.Type = "podman" },
			field:  "runtime.type",
		},
		{
			name:   "docker host empty",
			mutate: func(c *Config) { c.Runtime.DockerHost = "" },
			field:  "runtime.docker_host",
		},
		{
			name:   "default image empty",
			mutate: func(c *Config) { c.Runtime.DefaultImage = "" },
			field:  "runtime.default_image",
		},
		{
			name: "default image too long",
			mutate: func(c *Config) {
				c.Runtime.DefaultImage = strings.Repeat(
					"a",
					domain.MaxImageReferenceLength+1,
				)
			},
			field: "runtime.default_image",
		},
		{
			name: "runner socket directory relative",
			mutate: func(c *Config) {
				c.Runtime.RunnerSocketDirectory = "run"
			},
			field: "runtime.runner_socket_directory",
		},
		{
			name: "workspace directory relative",
			mutate: func(c *Config) {
				c.Runtime.WorkspaceDirectory = "workspaces"
			},
			field: "runtime.workspace_directory",
		},
		{
			name:   "network mode not none",
			mutate: func(c *Config) { c.Runtime.NetworkMode = "bridge" },
			field:  "runtime.network_mode",
		},
		{
			name:   "persistent workspace",
			mutate: func(c *Config) { c.Runtime.WorkspacePersistent = true },
			field:  "runtime.workspace_persistent",
		},
		{
			name:   "platform os not linux",
			mutate: func(c *Config) { c.Runtime.Platform.OS = "windows" },
			field:  "runtime.platform.os",
		},
		{
			name:   "platform arch not amd64",
			mutate: func(c *Config) { c.Runtime.Platform.Arch = "arm64" },
			field:  "runtime.platform.arch",
		},
		{
			name: "pre-pull platform differs from runtime",
			mutate: func(c *Config) {
				c.Runtime.PrePullImages = []PrePullImage{{Image: "alpine:3.22", Platform: "linux/arm64"}}
			},
			field: "runtime.prepull_images",
		},
		{
			name:   "default ttl not positive",
			mutate: func(c *Config) { c.Limits.DefaultTTL = 0 },
			field:  "limits.default_ttl",
		},
		{
			name:   "maximum ttl not positive",
			mutate: func(c *Config) { c.Limits.MaximumTTL = -time.Second },
			field:  "limits.maximum_ttl",
		},
		{
			name: "default ttl exceeds maximum",
			mutate: func(c *Config) {
				c.Limits.DefaultTTL = 48 * time.Hour
			},
			field: "limits.default_ttl",
		},
		{
			name: "max cpu not positive",
			mutate: func(c *Config) {
				c.Limits.MaxResources.MaxCPUQuotaMillis = 0
			},
			field: "limits.max_resources.cpu_quota_millis",
		},
		{
			name: "max memory not positive",
			mutate: func(c *Config) {
				c.Limits.MaxResources.MaxMemoryMiB = 0
			},
			field: "limits.max_resources.memory_mib",
		},
		{
			name:   "max pids not positive",
			mutate: func(c *Config) { c.Limits.MaxResources.MaxPIDs = -1 },
			field:  "limits.max_resources.pids",
		},
		{
			name: "default cpu above maximum",
			mutate: func(c *Config) {
				c.Limits.DefaultResources.CPUQuotaMillis = 4001
			},
			field: "limits.default_resources.cpu_quota_millis",
		},
		{
			name: "default memory not positive",
			mutate: func(c *Config) {
				c.Limits.DefaultResources.MemoryMiB = 0
			},
			field: "limits.default_resources.memory_mib",
		},
		{
			name: "default pids above maximum",
			mutate: func(c *Config) {
				c.Limits.DefaultResources.PIDs = 1025
			},
			field: "limits.default_resources.pids",
		},
		{
			name:   "reconcile interval not positive",
			mutate: func(c *Config) { c.Reconcile.Interval = 0 },
			field:  "reconcile.interval",
		},
		{
			name: "runner ready timeout not positive",
			mutate: func(c *Config) {
				c.Reconcile.RunnerReadyTimeout = -time.Second
			},
			field: "reconcile.runner_ready_timeout",
		},
		{
			name:   "deletion timeout not positive",
			mutate: func(c *Config) { c.Reconcile.DeletionTimeout = 0 },
			field:  "reconcile.deletion_timeout",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Default()
			test.mutate(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			var fieldErr *FieldError
			if !errors.As(err, &fieldErr) {
				t.Fatalf("error is not a FieldError: %v", err)
			}
			if got, want := fieldErr.Field, test.field; got != want {
				t.Fatalf("unexpected field: got %s, want %s", got, want)
			}
		})
	}
}

// TestValidatePhase3Rejections 逐条锁定 lease、scanner、retry、operation
// semaphore、idempotency retention 与 admin 的启动前 fail-closed 规则。
func TestValidatePhase3Rejections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		field  string
	}{
		{"minimum ttl zero", func(c *Config) { c.Limits.MinimumTTL = 0 }, "limits.minimum_ttl"},
		{"minimum ttl exceeds default", func(c *Config) { c.Limits.MinimumTTL = c.Limits.DefaultTTL + time.Second }, "limits.minimum_ttl"},
		{"max sandboxes zero", func(c *Config) { c.Limits.MaxSandboxes = 0 }, "limits.max_sandboxes"},
		{"max sandboxes unbounded", func(c *Config) { c.Limits.MaxSandboxes = maxSandboxes + 1 }, "limits.max_sandboxes"},
		{"create semaphore zero", func(c *Config) { c.Limits.MaxConcurrentCreates = 0 }, "limits.max_concurrent_creates"},
		{"create semaphore unbounded", func(c *Config) { c.Limits.MaxConcurrentCreates = maxLifecycleOperationConcurrency + 1 }, "limits.max_concurrent_creates"},
		{"pull semaphore zero", func(c *Config) { c.Limits.MaxConcurrentImagePulls = 0 }, "limits.max_concurrent_image_pulls"},
		{"pull semaphore unbounded", func(c *Config) { c.Limits.MaxConcurrentImagePulls = maxLifecycleOperationConcurrency + 1 }, "limits.max_concurrent_image_pulls"},
		{"delete semaphore zero", func(c *Config) { c.Limits.MaxConcurrentDeletes = 0 }, "limits.max_concurrent_deletes"},
		{"delete semaphore unbounded", func(c *Config) { c.Limits.MaxConcurrentDeletes = maxLifecycleOperationConcurrency + 1 }, "limits.max_concurrent_deletes"},
		{"jitter negative", func(c *Config) { c.Reconcile.Jitter = -time.Nanosecond }, "reconcile.jitter"},
		{"jitter exceeds interval", func(c *Config) { c.Reconcile.Jitter = c.Reconcile.Interval + time.Nanosecond }, "reconcile.jitter"},
		{"attempt timeout zero", func(c *Config) { c.Reconcile.Timeout = 0 }, "reconcile.timeout"},
		{"page size zero", func(c *Config) { c.Reconcile.PageSize = 0 }, "reconcile.page_size"},
		{"page size unbounded", func(c *Config) { c.Reconcile.PageSize = maxReconcilePageSize + 1 }, "reconcile.page_size"},
		{"worker count zero", func(c *Config) { c.Reconcile.MaxConcurrent = 0 }, "reconcile.max_concurrent"},
		{"worker count unbounded", func(c *Config) { c.Reconcile.MaxConcurrent = maxReconcileWorkers + 1 }, "reconcile.max_concurrent"},
		{"retry min zero", func(c *Config) { c.Reconcile.RetryMin = 0 }, "reconcile.retry_min"},
		{"retry max zero", func(c *Config) { c.Reconcile.RetryMax = 0 }, "reconcile.retry_max"},
		{"retry min exceeds max", func(c *Config) { c.Reconcile.RetryMin = c.Reconcile.RetryMax + time.Nanosecond }, "reconcile.retry_min"},
		{"running check zero", func(c *Config) { c.Reconcile.RunningCheckInterval = 0 }, "reconcile.running_check_interval"},
		{"unhealthy threshold zero", func(c *Config) { c.Reconcile.RunnerUnhealthyThreshold = 0 }, "reconcile.runner_unhealthy_threshold"},
		{"unhealthy threshold unbounded", func(c *Config) { c.Reconcile.RunnerUnhealthyThreshold = maxRunnerUnhealthyThreshold + 1 }, "reconcile.runner_unhealthy_threshold"},
		{"docker freshness zero", func(c *Config) { c.Reconcile.DockerFreshness = 0 }, "reconcile.docker_freshness"},
		{"idempotency key zero", func(c *Config) { c.Idempotency.MaxKeyBytes = 0 }, "idempotency.max_key_bytes"},
		{"idempotency key unbounded", func(c *Config) { c.Idempotency.MaxKeyBytes = maxIdempotencyKeyBytes + 1 }, "idempotency.max_key_bytes"},
		{"terminal retention zero", func(c *Config) { c.Idempotency.TerminalRetention = 0 }, "idempotency.terminal_retention"},
		{"terminal retention unbounded", func(c *Config) { c.Idempotency.TerminalRetention = maxIdempotencyTerminalRetention + time.Nanosecond }, "idempotency.terminal_retention"},
		{"idempotency gc zero", func(c *Config) { c.Idempotency.GCInterval = 0 }, "idempotency.gc_interval"},
		{"idempotency gc exceeds retention", func(c *Config) { c.Idempotency.GCInterval = c.Idempotency.TerminalRetention + time.Nanosecond }, "idempotency.gc_interval"},
		{"enabled admin token empty", func(c *Config) { c.Admin.Enabled = true }, "admin.token_file"},
		{"enabled admin token relative", func(c *Config) { c.Admin.Enabled = true; c.Admin.TokenFile = "admin-token" }, "admin.token_file"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Default()
			test.mutate(&cfg)
			var fieldErr *FieldError
			if err := cfg.Validate(); !errors.As(err, &fieldErr) {
				t.Fatalf("expected FieldError, got %v", err)
			}
			if fieldErr.Field != test.field {
				t.Fatalf("unexpected field: got %s, want %s", fieldErr.Field, test.field)
			}
		})
	}
}

// TestValidatePhase3Boundaries 验证闭区间边界以及 admin 关闭时完全忽略
// token path；enabled 的绝对路径也只做纯配置校验，不访问文件系统。
func TestValidatePhase3Boundaries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"minimum equals default and maximum", func(c *Config) { c.Limits.MinimumTTL = c.Limits.MaximumTTL; c.Limits.DefaultTTL = c.Limits.MaximumTTL }},
		{"jitter equals interval", func(c *Config) { c.Reconcile.Jitter = c.Reconcile.Interval }},
		{"retry min equals max", func(c *Config) { c.Reconcile.RetryMin = c.Reconcile.RetryMax }},
		{"bounded integer maxima", func(c *Config) {
			c.Limits.MaxSandboxes = maxSandboxes
			c.Limits.MaxConcurrentCreates = maxLifecycleOperationConcurrency
			c.Limits.MaxConcurrentImagePulls = maxLifecycleOperationConcurrency
			c.Limits.MaxConcurrentDeletes = maxLifecycleOperationConcurrency
			c.Reconcile.PageSize = maxReconcilePageSize
			c.Reconcile.MaxConcurrent = maxReconcileWorkers
			c.Reconcile.RunnerUnhealthyThreshold = maxRunnerUnhealthyThreshold
			c.Idempotency.MaxKeyBytes = maxIdempotencyKeyBytes
		}},
		{"gc equals retention", func(c *Config) { c.Idempotency.GCInterval = c.Idempotency.TerminalRetention }},
		{"admin disabled ignores token path", func(c *Config) { c.Admin.TokenFile = "relative-and-not-readable" }},
		{"admin enabled accepts nonexistent absolute path", func(c *Config) { c.Admin.Enabled = true; c.Admin.TokenFile = "/path/need/not/exist/during-validation" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Default()
			test.mutate(&cfg)
			if err := cfg.Validate(); err != nil {
				t.Fatalf("valid boundary rejected: %v", err)
			}
		})
	}
}

// TestValidateRunnerRejections 逐项锁定 runner 身份、路径和有界限制的
// fail-closed 规则，避免零值或过大配置绕过启动校验。
func TestValidateRunnerRejections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		field  string
	}{
		{"root execution uid", func(c *Config) { c.Runner.ExecutionUID = 0 }, "runner.execution_uid"},
		{"root execution gid", func(c *Config) { c.Runner.ExecutionGID = 0 }, "runner.execution_gid"},
		{"cwd outside workspace", func(c *Config) { c.Runner.DefaultCWD = "/tmp" }, "runner.default_cwd"},
		{"default timeout zero", func(c *Config) { c.Runner.DefaultTimeout = 0 }, "runner.default_timeout"},
		{"default timeout above max", func(c *Config) { c.Runner.DefaultTimeout = 2 * time.Hour; c.Runner.MaxTimeout = time.Hour }, "runner.default_timeout"},
		{"max timeout zero", func(c *Config) { c.Runner.MaxTimeout = 0 }, "runner.max_timeout"},
		{"max timeout unbounded", func(c *Config) { c.Runner.MaxTimeout = 24*time.Hour + time.Second }, "runner.max_timeout"},
		{"termination grace zero", func(c *Config) { c.Runner.TerminationGrace = 0 }, "runner.termination_grace"},
		{"termination grace unbounded", func(c *Config) { c.Runner.TerminationGrace = time.Minute + time.Millisecond }, "runner.termination_grace"},
		{"concurrency zero", func(c *Config) { c.Runner.MaxConcurrentExecutions = 0 }, "runner.max_concurrent_executions"},
		{"concurrency unbounded", func(c *Config) { c.Runner.MaxConcurrentExecutions = 257 }, "runner.max_concurrent_executions"},
		{"request bytes zero", func(c *Config) { c.Runner.MaxRequestBytes = 0 }, "runner.max_request_bytes"},
		{"request bytes unbounded", func(c *Config) { c.Runner.MaxRequestBytes = 16<<20 + 1 }, "runner.max_request_bytes"},
		{"output bytes zero", func(c *Config) { c.Runner.MaxOutputBytes = 0 }, "runner.max_output_bytes"},
		{"output bytes unbounded", func(c *Config) { c.Runner.MaxOutputBytes = 1<<30 + 1 }, "runner.max_output_bytes"},
		{"env vars zero", func(c *Config) { c.Runner.MaxEnvVars = 0 }, "runner.max_env_vars"},
		{"env vars unbounded", func(c *Config) { c.Runner.MaxEnvVars = 4097 }, "runner.max_env_vars"},
		{"env key bytes zero", func(c *Config) { c.Runner.MaxEnvKeyBytes = 0 }, "runner.max_env_key_bytes"},
		{"env key bytes unbounded", func(c *Config) { c.Runner.MaxEnvKeyBytes = 1025 }, "runner.max_env_key_bytes"},
		{"env value bytes zero", func(c *Config) { c.Runner.MaxEnvValueBytes = 0 }, "runner.max_env_value_bytes"},
		{"env value bytes unbounded", func(c *Config) { c.Runner.MaxEnvValueBytes = 1<<20 + 1 }, "runner.max_env_value_bytes"},
		{"env total bytes zero", func(c *Config) { c.Runner.MaxEnvTotalBytes = 0 }, "runner.max_env_total_bytes"},
		{"env total bytes unbounded", func(c *Config) { c.Runner.MaxEnvTotalBytes = 16<<20 + 1 }, "runner.max_env_total_bytes"},
		{"env total cannot hold entry", func(c *Config) { c.Runner.MaxEnvTotalBytes = 100 }, "runner.max_env_total_bytes"},
		{"env total exceeds request", func(c *Config) { c.Runner.MaxRequestBytes = 1024; c.Runner.MaxEnvTotalBytes = 2048 }, "runner.max_env_total_bytes"},
		{"log events zero", func(c *Config) { c.Runner.MaxLogPageEvents = 0 }, "runner.max_log_page_events"},
		{"log events unbounded", func(c *Config) { c.Runner.MaxLogPageEvents = 4097 }, "runner.max_log_page_events"},
		{"log page bytes zero", func(c *Config) { c.Runner.MaxLogPageBytes = 0 }, "runner.max_log_page_bytes"},
		{"log page bytes unbounded", func(c *Config) { c.Runner.MaxLogPageBytes = 64<<20 + 1 }, "runner.max_log_page_bytes"},
		{"log page exceeds output", func(c *Config) { c.Runner.MaxOutputBytes = 1024; c.Runner.MaxLogPageBytes = 2048 }, "runner.max_log_page_bytes"},
		{"retention zero", func(c *Config) { c.Runner.CompletedRetention = 0 }, "runner.completed_retention"},
		{"retention unbounded", func(c *Config) { c.Runner.CompletedRetention = 7*24*time.Hour + time.Second }, "runner.completed_retention"},
		{"retained executions zero", func(c *Config) { c.Runner.MaxRetainedExecutions = 0 }, "runner.max_retained_executions"},
		{"retained executions unbounded", func(c *Config) { c.Runner.MaxRetainedExecutions = 10_001 }, "runner.max_retained_executions"},
		{"sse timeout zero", func(c *Config) { c.Runner.SSEWriteTimeout = 0 }, "runner.sse_write_timeout"},
		{"sse timeout unbounded", func(c *Config) { c.Runner.SSEWriteTimeout = time.Minute + time.Millisecond }, "runner.sse_write_timeout"},
		{"master key path relative", func(c *Config) { c.Security.RunnerMasterKeyFile = "runner.key" }, "security.runner_master_key_file"},
		{"outbound missing sidecar image", func(c *Config) { c.Security.AllowOutbound = true }, "egress.image"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Default()
			test.mutate(&cfg)
			var fieldErr *FieldError
			if err := cfg.Validate(); !errors.As(err, &fieldErr) {
				t.Fatalf("expected FieldError, got %v", err)
			}
			if got := fieldErr.Field; got != test.field {
				t.Fatalf("unexpected field: got %s, want %s", got, test.field)
			}
		})
	}
}

// TestValidateEgressRejections 锁定 sidecar artifact、协议、超时、身份、附加
// deny CIDR 和资源上限，避免不完整或可漂移配置进入 reconcile。
func TestValidateEgressRejections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		field  string
	}{
		{"tag-only image", func(c *Config) { c.Egress.Image = "registry.example/egressd:latest" }, "egress.image"},
		{"uppercase digest", func(c *Config) { c.Egress.Image = "registry.example/egressd@sha256:" + strings.Repeat("A", 64) }, "egress.image"},
		{"unsupported protocol", func(c *Config) { c.Egress.ProtocolVersion++ }, "egress.protocol_version"},
		{"ready timeout too short", func(c *Config) { c.Egress.ReadyTimeout = time.Second - time.Nanosecond }, "egress.ready_timeout"},
		{"ready timeout too long", func(c *Config) { c.Egress.ReadyTimeout = 2*time.Minute + time.Nanosecond }, "egress.ready_timeout"},
		{"root anchor uid", func(c *Config) { c.Egress.AnchorUID = 0 }, "egress.anchor_uid"},
		{"root anchor gid", func(c *Config) { c.Egress.AnchorGID = 0 }, "egress.anchor_gid"},
		{"empty denied CIDR", func(c *Config) { c.Egress.DeniedCIDRs = []string{""} }, "egress.egress_denied_cidrs"},
		{"invalid denied CIDR", func(c *Config) { c.Egress.DeniedCIDRs = []string{"invalid-cidr-canary"} }, "egress.egress_denied_cidrs"},
		{"sidecar cpu zero", func(c *Config) { c.Egress.Limits.CPUQuotaMillis = 0 }, "egress.limits.cpu_quota_millis"},
		{"sidecar cpu too large", func(c *Config) { c.Egress.Limits.CPUQuotaMillis = 1001 }, "egress.limits.cpu_quota_millis"},
		{"sidecar memory zero", func(c *Config) { c.Egress.Limits.MemoryMiB = 0 }, "egress.limits.memory_mib"},
		{"sidecar memory too large", func(c *Config) { c.Egress.Limits.MemoryMiB = 513 }, "egress.limits.memory_mib"},
		{"sidecar pids zero", func(c *Config) { c.Egress.Limits.PIDs = 0 }, "egress.limits.pids"},
		{"sidecar pids too large", func(c *Config) { c.Egress.Limits.PIDs = 65 }, "egress.limits.pids"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Default()
			test.mutate(&cfg)
			var fieldErr *FieldError
			if err := cfg.Validate(); !errors.As(err, &fieldErr) {
				t.Fatalf("expected FieldError, got %v", err)
			}
			if fieldErr.Field != test.field {
				t.Fatalf("unexpected field: got %s, want %s", fieldErr.Field, test.field)
			}
			if strings.Contains(fieldErr.Error(), "invalid-cidr-canary") {
				t.Fatalf("validation error leaks rejected policy input: %v", fieldErr)
			}
		})
	}
}

// TestValidateRunnerSocketOwner 验证 execution UID/GID 不能复用控制面
// Unix Socket 的数字所有者身份。
func TestValidateRunnerSocketOwner(t *testing.T) {
	tests := []struct {
		name  string
		uid   uint32
		gid   uint32
		field string
	}{
		{"distinct identity", 2000, 2001, ""},
		{"same uid", 1000, 2001, "runner.execution_uid"},
		{"same gid", 2000, 1000, "runner.execution_gid"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Default().ValidateRunnerSocketOwner(test.uid, test.gid)
			if test.field == "" {
				if err != nil {
					t.Fatalf("distinct identity rejected: %v", err)
				}
				return
			}
			var fieldErr *FieldError
			if !errors.As(err, &fieldErr) || fieldErr.Field != test.field {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestValidateAccepts 验证安全默认值与合法边界取值可以通过校验。
func TestValidateAccepts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{
			name:   "default config",
			mutate: func(*Config) {},
		},
		{
			name: "localhost hostname",
			mutate: func(c *Config) {
				c.Server.ListenAddress = "localhost:8080"
			},
		},
		{
			name: "ipv6 loopback",
			mutate: func(c *Config) {
				c.Server.ListenAddress = "[::1]:8080"
			},
		},
		{
			name: "default resources equal maximum",
			mutate: func(c *Config) {
				c.Limits.DefaultResources = domain.ResourceLimits{
					CPUQuotaMillis: 4000,
					MemoryMiB:      8192,
					PIDs:           1024,
				}
			},
		},
		{
			name: "default ttl equals maximum",
			mutate: func(c *Config) {
				c.Limits.DefaultTTL = c.Limits.MaximumTTL
			},
		},
		{
			name: "outbound with immutable sidecar digest",
			mutate: func(c *Config) {
				c.Security.AllowOutbound = true
				c.Egress.Image = "registry.example/minisandbox/egressd@sha256:" + strings.Repeat("a", 64)
				c.Egress.DeniedCIDRs = []string{"8.8.8.0/24", "2001:4860::/32"}
			},
		},
		{
			name: "egress safe upper bounds",
			mutate: func(c *Config) {
				c.Egress.ReadyTimeout = 2 * time.Minute
				c.Egress.Limits = domain.ResourceLimits{CPUQuotaMillis: 1000, MemoryMiB: 512, PIDs: 64}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Default()
			test.mutate(&cfg)

			if err := cfg.Validate(); err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

// TestValidateExampleConfig 保证仓库示例配置始终可加载且通过安全校验。
func TestValidateExampleConfig(t *testing.T) {
	examplePath := filepath.Join(
		"..",
		"..",
		"configs",
		"sandboxd.example.yaml",
	)

	cfg, err := Load(examplePath)
	if err != nil {
		t.Fatalf("load example config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("example config fails validation: %v", err)
	}
}
