package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/domain"
)

// writeConfigFile 把配置内容写入临时文件并返回路径。
func writeConfigFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sandboxd.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	return path
}

// TestLoadMinimalConfig 验证最小配置只覆盖出现的字段,其余保持默认值。
func TestLoadMinimalConfig(t *testing.T) {
	t.Run("single field", func(t *testing.T) {
		path := writeConfigFile(t, "runtime:\n  default_image: \"alpine:3.22\"\n")

		got, err := Load(path)
		if err != nil {
			t.Fatalf("load minimal config: %v", err)
		}

		want := Default()
		want.Runtime.DefaultImage = "alpine:3.22"
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("minimal config mismatch:\ngot  %+v\nwant %+v", got, want)
		}
	})

	t.Run("explicit zero value overrides", func(t *testing.T) {
		path := writeConfigFile(t, "server:\n  listen_address: \"\"\n")

		got, err := Load(path)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if got.Server.ListenAddress != "" {
			t.Fatalf(
				"explicit empty value not applied: got %q",
				got.Server.ListenAddress,
			)
		}
	})

	t.Run("empty file equals defaults", func(t *testing.T) {
		path := writeConfigFile(t, "")

		got, err := Load(path)
		if err != nil {
			t.Fatalf("load empty config: %v", err)
		}
		if !reflect.DeepEqual(got, Default()) {
			t.Fatalf("empty config is not default:\ngot %+v", got)
		}
	})
}

// TestLoadFullConfig 验证每个字段都能从文件覆盖。
//
// 所有取值都刻意区别于 Default,以便任何一个字段覆盖失效都会被发现;
// 取值本身是否合法不在加载层职责内,由后续配置校验拒绝。
func TestLoadFullConfig(t *testing.T) {
	path := writeConfigFile(t, `server:
  listen_address: "127.0.0.1:9090"
  shutdown_timeout: "5s"
data:
  directory: "/srv/minisandbox"
  sqlite_path: "/srv/minisandbox/state.db"
runtime:
  type: "docker-test"
  docker_host: "unix:///run/docker.sock"
  default_image: "alpine:3.22"
  runner_socket_directory: "/srv/minisandbox/run"
  workspace_directory: "/srv/minisandbox/workspaces"
  network_mode: "bridge"
  workspace_persistent: true
  platform:
    os: "freebsd"
    arch: "arm64"
limits:
  default_ttl: "10m"
  maximum_ttl: "2h"
  default_resources:
    cpu_quota_millis: 250
    memory_mib: 128
    pids: 32
  max_resources:
    cpu_quota_millis: 1000
    memory_mib: 2048
    pids: 512
runner:
  execution_uid: 2000
  execution_gid: 2001
  default_cwd: "/workspace/jobs"
  default_timeout: "30s"
  max_timeout: "5m"
  termination_grace: "3s"
  max_concurrent_executions: 4
  max_request_bytes: 2048
  max_output_bytes: 4096
  max_env_vars: 16
  max_env_key_bytes: 64
  max_env_value_bytes: 512
  max_env_total_bytes: 1024
  max_log_page_events: 32
  max_log_page_bytes: 2048
  completed_retention: "30m"
  max_retained_executions: 25
  sse_write_timeout: "4s"
reconcile:
  interval: "5s"
  runner_ready_timeout: "20s"
  deletion_timeout: "45s"
`)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load full config: %v", err)
	}

	want := Config{
		Server: ServerConfig{
			ListenAddress:   "127.0.0.1:9090",
			ShutdownTimeout: 5 * time.Second,
		},
		Data: DataConfig{
			Directory:  "/srv/minisandbox",
			SQLitePath: "/srv/minisandbox/state.db",
		},
		Runtime: RuntimeConfig{
			Type:                  "docker-test",
			DockerHost:            "unix:///run/docker.sock",
			DefaultImage:          "alpine:3.22",
			RunnerSocketDirectory: "/srv/minisandbox/run",
			WorkspaceDirectory:    "/srv/minisandbox/workspaces",
			NetworkMode:           "bridge",
			WorkspacePersistent:   true,
			Platform: domain.Platform{
				OS:   "freebsd",
				Arch: "arm64",
			},
		},
		Limits: LimitsConfig{
			DefaultTTL: 10 * time.Minute,
			MaximumTTL: 2 * time.Hour,
			DefaultResources: domain.ResourceLimits{
				CPUQuotaMillis: 250,
				MemoryMiB:      128,
				PIDs:           32,
			},
			MaxResources: domain.ResourceBounds{
				MaxCPUQuotaMillis: 1000,
				MaxMemoryMiB:      2048,
				MaxPIDs:           512,
			},
		},
		Runner: RunnerConfig{
			ExecutionUID:            2000,
			ExecutionGID:            2001,
			DefaultCWD:              "/workspace/jobs",
			DefaultTimeout:          30 * time.Second,
			MaxTimeout:              5 * time.Minute,
			TerminationGrace:        3 * time.Second,
			MaxConcurrentExecutions: 4,
			MaxRequestBytes:         2048,
			MaxOutputBytes:          4096,
			MaxEnvVars:              16,
			MaxEnvKeyBytes:          64,
			MaxEnvValueBytes:        512,
			MaxEnvTotalBytes:        1024,
			MaxLogPageEvents:        32,
			MaxLogPageBytes:         2048,
			CompletedRetention:      30 * time.Minute,
			MaxRetainedExecutions:   25,
			SSEWriteTimeout:         4 * time.Second,
		},
		Reconcile: ReconcileConfig{
			Interval:           5 * time.Second,
			RunnerReadyTimeout: 20 * time.Second,
			DeletionTimeout:    45 * time.Second,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("full config mismatch:\ngot  %+v\nwant %+v", got, want)
	}
}

// TestLoadUnknownField 验证未知字段报错、错误含位置且不回显其他配置内容。
func TestLoadUnknownField(t *testing.T) {
	const canary = "canary-value-must-not-leak"

	path := writeConfigFile(t, `data:
  directory: "/`+canary+`"
server:
  listen: "127.0.0.1:8080"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected unknown field error")
	}
	message := err.Error()
	if !strings.Contains(message, "listen") {
		t.Fatalf("error does not locate unknown field: %v", err)
	}
	if !strings.Contains(message, "line") {
		t.Fatalf("error does not contain line position: %v", err)
	}
	if strings.Contains(message, canary) {
		t.Fatalf("error echoes unrelated config content: %v", err)
	}
}

// TestLoadMalformedYAML 验证格式错误返回带位置的解析错误。
func TestLoadMalformedYAML(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "unterminated quote",
			content: "server:\n  listen_address: \"127.0.0.1\n",
		},
		{
			name:    "wrong scalar type",
			content: "runtime:\n  workspace_persistent: \"not-a-bool\"\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeConfigFile(t, test.content)

			_, err := Load(path)
			if err == nil {
				t.Fatal("expected parse error")
			}
			if !strings.Contains(err.Error(), "line") {
				t.Fatalf("error does not contain line position: %v", err)
			}
		})
	}
}

// TestLoadInvalidDuration 验证 duration 错误定位字段且不回显原始值。
func TestLoadInvalidDuration(t *testing.T) {
	const bogus = "totally-bogus-duration"

	path := writeConfigFile(
		t,
		"server:\n  shutdown_timeout: \""+bogus+"\"\n",
	)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected duration error")
	}
	if !strings.Contains(err.Error(), "server.shutdown_timeout") {
		t.Fatalf("error does not locate duration field: %v", err)
	}
	if strings.Contains(err.Error(), bogus) {
		t.Fatalf("error echoes raw duration value: %v", err)
	}
}

// TestLoadMissingFile 验证文件不存在时返回读取错误。
func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("expected read error for missing file")
	}
}

// TestLoadMultipleDocuments 验证多文档 YAML 被拒绝。
func TestLoadMultipleDocuments(t *testing.T) {
	path := writeConfigFile(
		t,
		"server:\n  listen_address: \"127.0.0.1:8080\"\n---\ndata: {}\n",
	)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected multiple document error")
	}
	if !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("unexpected error: %v", err)
	}
}
