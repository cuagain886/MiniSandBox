package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
)

// fileConfig 是 YAML 配置文件的解码模型。
//
// 所有字段都使用指针,以区分"文件未出现该字段"(保持默认值)与"显式设置
// 零值";duration 先解码为字符串,再统一解析并生成带字段路径的错误。
// 本模型只服务于文件解码,不得被 Load 之外的代码使用。
type fileConfig struct {
	Server    *fileServer    `yaml:"server"`
	Data      *fileData      `yaml:"data"`
	Runtime   *fileRuntime   `yaml:"runtime"`
	Limits    *fileLimits    `yaml:"limits"`
	Reconcile *fileReconcile `yaml:"reconcile"`
}

// fileServer 对应 server 分组的文件字段。
type fileServer struct {
	ListenAddress   *string `yaml:"listen_address"`
	ShutdownTimeout *string `yaml:"shutdown_timeout"`
}

// fileData 对应 data 分组的文件字段。
type fileData struct {
	Directory  *string `yaml:"directory"`
	SQLitePath *string `yaml:"sqlite_path"`
}

// fileRuntime 对应 runtime 分组的文件字段。
type fileRuntime struct {
	Type                  *string       `yaml:"type"`
	DockerHost            *string       `yaml:"docker_host"`
	DefaultImage          *string       `yaml:"default_image"`
	RunnerSocketDirectory *string       `yaml:"runner_socket_directory"`
	WorkspaceDirectory    *string       `yaml:"workspace_directory"`
	NetworkMode           *string       `yaml:"network_mode"`
	WorkspacePersistent   *bool         `yaml:"workspace_persistent"`
	Platform              *filePlatform `yaml:"platform"`
}

// filePlatform 对应 runtime.platform 的文件字段。
type filePlatform struct {
	OS   *string `yaml:"os"`
	Arch *string `yaml:"arch"`
}

// fileLimits 对应 limits 分组的文件字段。
type fileLimits struct {
	DefaultTTL       *string        `yaml:"default_ttl"`
	MaximumTTL       *string        `yaml:"maximum_ttl"`
	DefaultResources *fileResources `yaml:"default_resources"`
	MaxResources     *fileResources `yaml:"max_resources"`
}

// fileResources 对应资源三元组的文件字段,default_resources 与
// max_resources 复用同一形状。
type fileResources struct {
	CPUQuotaMillis *int64 `yaml:"cpu_quota_millis"`
	MemoryMiB      *int64 `yaml:"memory_mib"`
	PIDs           *int64 `yaml:"pids"`
}

// fileReconcile 对应 reconcile 分组的文件字段。
type fileReconcile struct {
	Interval           *string `yaml:"interval"`
	RunnerReadyTimeout *string `yaml:"runner_ready_timeout"`
	DeletionTimeout    *string `yaml:"deletion_timeout"`
}

// Load 从显式路径读取 YAML 配置,并把文件中出现的字段覆盖到安全默认值上。
//
// 未出现的字段保持 Default 的取值;空文件等价于全部默认值。未知字段、
// 格式错误和非法 duration 都返回带位置或字段路径的错误,但不回显整个
// 配置内容。本函数不创建目录、不连接外部依赖,也不做跨字段安全校验。
func Load(path string) (Config, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(content))
	// KnownFields 拒绝未知字段,防止拼写错误被静默忽略。
	decoder.KnownFields(true)

	var file fileConfig
	if err := decoder.Decode(&file); err != nil {
		if errors.Is(err, io.EOF) {
			return Default(), nil
		}
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	// 配置必须是单一 YAML 文档,多文档说明文件被误用。
	if err := decoder.Decode(new(fileConfig)); !errors.Is(err, io.EOF) {
		return Config{}, errors.New(
			"parse config: multiple YAML documents are not supported",
		)
	}

	return file.apply(Default())
}

// apply 把文件中出现的字段覆盖到 base 配置上并返回结果。
func (f fileConfig) apply(base Config) (Config, error) {
	cfg := base

	if f.Server != nil {
		override(&cfg.Server.ListenAddress, f.Server.ListenAddress)
		if err := overrideDuration(
			&cfg.Server.ShutdownTimeout,
			f.Server.ShutdownTimeout,
			"server.shutdown_timeout",
		); err != nil {
			return Config{}, err
		}
	}

	if f.Data != nil {
		override(&cfg.Data.Directory, f.Data.Directory)
		override(&cfg.Data.SQLitePath, f.Data.SQLitePath)
	}

	if f.Runtime != nil {
		override(&cfg.Runtime.Type, f.Runtime.Type)
		override(&cfg.Runtime.DockerHost, f.Runtime.DockerHost)
		override(&cfg.Runtime.DefaultImage, f.Runtime.DefaultImage)
		override(
			&cfg.Runtime.RunnerSocketDirectory,
			f.Runtime.RunnerSocketDirectory,
		)
		override(&cfg.Runtime.WorkspaceDirectory, f.Runtime.WorkspaceDirectory)
		override(&cfg.Runtime.NetworkMode, f.Runtime.NetworkMode)
		override(&cfg.Runtime.WorkspacePersistent, f.Runtime.WorkspacePersistent)
		if f.Runtime.Platform != nil {
			override(&cfg.Runtime.Platform.OS, f.Runtime.Platform.OS)
			override(&cfg.Runtime.Platform.Arch, f.Runtime.Platform.Arch)
		}
	}

	if f.Limits != nil {
		if err := overrideDuration(
			&cfg.Limits.DefaultTTL,
			f.Limits.DefaultTTL,
			"limits.default_ttl",
		); err != nil {
			return Config{}, err
		}
		if err := overrideDuration(
			&cfg.Limits.MaximumTTL,
			f.Limits.MaximumTTL,
			"limits.maximum_ttl",
		); err != nil {
			return Config{}, err
		}
		if f.Limits.DefaultResources != nil {
			override(
				&cfg.Limits.DefaultResources.CPUQuotaMillis,
				f.Limits.DefaultResources.CPUQuotaMillis,
			)
			override(
				&cfg.Limits.DefaultResources.MemoryMiB,
				f.Limits.DefaultResources.MemoryMiB,
			)
			override(&cfg.Limits.DefaultResources.PIDs, f.Limits.DefaultResources.PIDs)
		}
		if f.Limits.MaxResources != nil {
			override(
				&cfg.Limits.MaxResources.MaxCPUQuotaMillis,
				f.Limits.MaxResources.CPUQuotaMillis,
			)
			override(
				&cfg.Limits.MaxResources.MaxMemoryMiB,
				f.Limits.MaxResources.MemoryMiB,
			)
			override(&cfg.Limits.MaxResources.MaxPIDs, f.Limits.MaxResources.PIDs)
		}
	}

	if f.Reconcile != nil {
		if err := overrideDuration(
			&cfg.Reconcile.Interval,
			f.Reconcile.Interval,
			"reconcile.interval",
		); err != nil {
			return Config{}, err
		}
		if err := overrideDuration(
			&cfg.Reconcile.RunnerReadyTimeout,
			f.Reconcile.RunnerReadyTimeout,
			"reconcile.runner_ready_timeout",
		); err != nil {
			return Config{}, err
		}
		if err := overrideDuration(
			&cfg.Reconcile.DeletionTimeout,
			f.Reconcile.DeletionTimeout,
			"reconcile.deletion_timeout",
		); err != nil {
			return Config{}, err
		}
	}

	return cfg, nil
}

// override 在文件字段出现时覆盖默认值,未出现时保持原值。
func override[T any](target *T, value *T) {
	if value != nil {
		*target = *value
	}
}

// overrideDuration 解析并覆盖 duration 字段。
//
// 错误只包含字段路径,不回显原始取值,避免异常配置内容进入日志或响应。
func overrideDuration(target *time.Duration, value *string, field string) error {
	if value == nil {
		return nil
	}
	parsed, err := time.ParseDuration(*value)
	if err != nil {
		return fmt.Errorf("%s: invalid duration", field)
	}
	*target = parsed
	return nil
}
