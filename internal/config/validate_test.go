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
