// Package runnerbootstrap 定义 sandboxd 交给单个 runnerd 的严格内部启动协议。
//
// 本包只承载由控制面配置、宿主机有效身份和 sandbox ID 推导的可信字段，
// 不承载用户 execution 请求，也不决定配置通过何种文件描述符或挂载传输。
package runnerbootstrap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"minisandbox/internal/config"
)

const (
	// CurrentProtocolVersion 是当前 runner bootstrap 与 health 协议的精确版本。
	// 不兼容修改必须递增该整数，控制面不得自动接受旧版或未来版本。
	CurrentProtocolVersion = 1
	// RuntimeDirectory 是 runner 容器内受管运行时目录。
	RuntimeDirectory = "/run/minisandbox"
	// SocketPath 是 runner 仅供控制面访问的 Unix Socket 路径。
	SocketPath = "/run/minisandbox/runner.sock"
	// ExecutionDataDirectory 是后台 execution 状态和日志的容器内受管目录。
	ExecutionDataDirectory = "/run/minisandbox/executions"
	// ConfigFileName 是宿主机经受管 bind mount 交给 runnerd 的一次性可信配置文件名。
	ConfigFileName = "bootstrap.json"
)

// Config 是单个 runnerd 启动所需的完整可信配置。
type Config struct {
	// ProtocolVersion 是必须与控制面和容器 label 精确匹配的协议版本。
	ProtocolVersion int `json:"protocol_version"`
	// SandboxID 是当前 runner 唯一允许服务的 sandbox 标识。
	SandboxID string `json:"sandbox_id"`
	// Identity 固定 socket owner 与永久降权后的 execution 数字身份。
	Identity Identity `json:"identity"`
	// Limits 是不能被普通 execution 请求扩大的服务端上限。
	Limits Limits `json:"limits"`
	// Paths 是 runner 可访问的固定容器内路径。
	Paths Paths `json:"paths"`
	// Features 是 Phase 4 能力开关与传输、会话和端口上限。
	Features Features `json:"features"`
}

// Features 描述控制面固定下发的 Phase 4 能力开关与上限。
//
// 关闭的能力在 runner 内对应 endpoint 返回明确 unavailable；普通请求不能
// 通过任何方式扩大这里的上限。
type Features struct {
	// FilesEnabled 表示 workspace 文件能力是否启用。
	FilesEnabled bool `json:"files_enabled"`
	// MaxUploadBytes 是单次上传字节上限。
	MaxUploadBytes int64 `json:"max_upload_bytes"`
	// MaxDownloadBytes 是单次下载字节上限。
	MaxDownloadBytes int64 `json:"max_download_bytes"`
	// PTYEnabled 表示交互终端能力是否启用。
	PTYEnabled bool `json:"pty_enabled"`
	// PTYMaxConcurrentSessions 是单 sandbox 并发 PTY 会话上限。
	PTYMaxConcurrentSessions int `json:"pty_max_concurrent_sessions"`
	// PTYDefaultTimeoutNanoseconds 是未指定 timeout 时的 PTY 默认时长。
	PTYDefaultTimeoutNanoseconds time.Duration `json:"pty_default_timeout_nanoseconds"`
	// PortProxyEnabled 表示 loopback HTTP 代理能力是否启用。
	PortProxyEnabled bool `json:"port_proxy_enabled"`
	// PortProxyMinPort 是允许代理的最小 TCP 端口。
	PortProxyMinPort int `json:"port_proxy_min_port"`
	// PortProxyMaxPort 是允许代理的最大 TCP 端口。
	PortProxyMaxPort int `json:"port_proxy_max_port"`
}

// Identity 描述控制面 socket owner 与 runner execution 的 Linux 数字身份。
type Identity struct {
	// ExecutionUID 是 runner 永久降权后使用的非零 UID。
	ExecutionUID uint32 `json:"execution_uid"`
	// ExecutionGID 是 runner 永久降权后使用的非零 GID。
	ExecutionGID uint32 `json:"execution_gid"`
	// SocketOwnerUID 是 sandboxd 宿主机有效 UID 的数字映射。
	SocketOwnerUID uint32 `json:"socket_owner_uid"`
	// SocketOwnerGID 是 sandboxd 宿主机有效 GID 的数字映射。
	SocketOwnerGID uint32 `json:"socket_owner_gid"`
}

// Limits 描述 runner 的有界 execution、输出、环境、日志和保留限制。
// duration 字段在 JSON 中显式使用纳秒单位，与 Go time.Duration 一致。
type Limits struct {
	// DefaultTimeoutNanoseconds 是未指定 timeout 时的纳秒数。
	DefaultTimeoutNanoseconds time.Duration `json:"default_timeout_nanoseconds"`
	// MaxTimeoutNanoseconds 是单次 execution timeout 的纳秒上限。
	MaxTimeoutNanoseconds time.Duration `json:"max_timeout_nanoseconds"`
	// TerminationGraceNanoseconds 是 TERM 到 KILL 的纳秒等待时长。
	TerminationGraceNanoseconds time.Duration `json:"termination_grace_nanoseconds"`
	// MaxConcurrentExecutions 是单 sandbox 并发 execution 上限。
	MaxConcurrentExecutions int `json:"max_concurrent_executions"`
	// MaxRequestBytes 是请求体字节上限。
	MaxRequestBytes int64 `json:"max_request_bytes"`
	// MaxOutputBytes 是单次 execution 保留输出字节上限。
	MaxOutputBytes int64 `json:"max_output_bytes"`
	// MaxEnvVars 是环境变量条目上限。
	MaxEnvVars int `json:"max_env_vars"`
	// MaxEnvKeyBytes 是单个环境变量名称字节上限。
	MaxEnvKeyBytes int `json:"max_env_key_bytes"`
	// MaxEnvValueBytes 是单个环境变量值字节上限。
	MaxEnvValueBytes int `json:"max_env_value_bytes"`
	// MaxEnvTotalBytes 是环境变量名称和值合计字节上限。
	MaxEnvTotalBytes int64 `json:"max_env_total_bytes"`
	// MaxLogPageEvents 是单页日志事件数上限。
	MaxLogPageEvents int `json:"max_log_page_events"`
	// MaxLogPageBytes 是单页日志编码前字节上限。
	MaxLogPageBytes int64 `json:"max_log_page_bytes"`
	// CompletedRetentionNanoseconds 是 terminal execution 保留纳秒数。
	CompletedRetentionNanoseconds time.Duration `json:"completed_retention_nanoseconds"`
	// MaxRetainedExecutions 是 terminal execution 保留数量上限。
	MaxRetainedExecutions int `json:"max_retained_executions"`
	// SSEWriteTimeoutNanoseconds 是单次 SSE 写出的纳秒超时。
	SSEWriteTimeoutNanoseconds time.Duration `json:"sse_write_timeout_nanoseconds"`
}

// Paths 描述 runner 容器内的固定受管路径。
type Paths struct {
	// WorkspaceDirectory 是 execution 唯一允许作为默认 cwd 的 workspace 根。
	WorkspaceDirectory string `json:"workspace_directory"`
	// RuntimeDirectory 是 socket 与 runner 数据的受管父目录。
	RuntimeDirectory string `json:"runtime_directory"`
	// SocketPath 是 runner HTTP Unix Socket 路径。
	SocketPath string `json:"socket_path"`
	// ExecutionDataDirectory 是后台状态和日志目录。
	ExecutionDataDirectory string `json:"execution_data_directory"`
}

// FromConfig 从已解析的控制面配置、sandbox ID 和 sandboxd 有效数字身份
// 构造完整 bootstrap 配置。
//
// 构造前复用启动校验并拒绝 socket owner 与 execution 身份重合；函数不读取
// 用户请求，也不访问文件系统。
func FromConfig(control config.Config, sandboxID string, socketOwnerUID, socketOwnerGID uint32) (Config, error) {
	if err := control.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate control config: %w", err)
	}
	if err := control.ValidateRunnerSocketOwner(socketOwnerUID, socketOwnerGID); err != nil {
		return Config{}, fmt.Errorf("validate runner identity: %w", err)
	}
	if strings.TrimSpace(sandboxID) == "" || strings.ContainsAny(sandboxID, "/\\") {
		return Config{}, errors.New("sandbox ID must be non-empty and contain no path separator")
	}

	runner := control.Runner
	return Config{
		ProtocolVersion: CurrentProtocolVersion,
		SandboxID:       sandboxID,
		Identity: Identity{
			ExecutionUID:   runner.ExecutionUID,
			ExecutionGID:   runner.ExecutionGID,
			SocketOwnerUID: socketOwnerUID,
			SocketOwnerGID: socketOwnerGID,
		},
		Limits: Limits{
			DefaultTimeoutNanoseconds:     runner.DefaultTimeout,
			MaxTimeoutNanoseconds:         runner.MaxTimeout,
			TerminationGraceNanoseconds:   runner.TerminationGrace,
			MaxConcurrentExecutions:       runner.MaxConcurrentExecutions,
			MaxRequestBytes:               runner.MaxRequestBytes,
			MaxOutputBytes:                runner.MaxOutputBytes,
			MaxEnvVars:                    runner.MaxEnvVars,
			MaxEnvKeyBytes:                runner.MaxEnvKeyBytes,
			MaxEnvValueBytes:              runner.MaxEnvValueBytes,
			MaxEnvTotalBytes:              runner.MaxEnvTotalBytes,
			MaxLogPageEvents:              runner.MaxLogPageEvents,
			MaxLogPageBytes:               runner.MaxLogPageBytes,
			CompletedRetentionNanoseconds: runner.CompletedRetention,
			MaxRetainedExecutions:         runner.MaxRetainedExecutions,
			SSEWriteTimeoutNanoseconds:    runner.SSEWriteTimeout,
		},
		Paths: Paths{
			WorkspaceDirectory:     runner.DefaultCWD,
			RuntimeDirectory:       RuntimeDirectory,
			SocketPath:             SocketPath,
			ExecutionDataDirectory: ExecutionDataDirectory,
		},
		Features: Features{
			FilesEnabled:                 control.Files.Enabled,
			MaxUploadBytes:               control.Files.MaxUploadBytes,
			MaxDownloadBytes:             control.Files.MaxDownloadBytes,
			PTYEnabled:                   control.PTY.Enabled,
			PTYMaxConcurrentSessions:     control.PTY.MaxConcurrentSessions,
			PTYDefaultTimeoutNanoseconds: control.PTY.DefaultTimeout,
			PortProxyEnabled:             control.PortProxy.Enabled,
			PortProxyMinPort:             control.PortProxy.MinPort,
			PortProxyMaxPort:             control.PortProxy.MaxPort,
		},
	}, nil
}

// Marshal 将完整 bootstrap 配置编码为单个 JSON 文档。
func Marshal(value Config) ([]byte, error) {
	if err := value.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// Unmarshal 严格解码单个 bootstrap JSON 文档。
//
// 未知字段、缺失字段、尾随 JSON 和不兼容版本均被拒绝，防止控制面与
// runner 对同一配置产生不同解释。
func Unmarshal(data []byte) (Config, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire wireConfig
	if err := decoder.Decode(&wire); err != nil {
		return Config{}, fmt.Errorf("decode runner bootstrap: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("decode runner bootstrap: trailing JSON value")
	}
	value, err := wire.value()
	if err != nil {
		return Config{}, err
	}
	if err := value.validate(); err != nil {
		return Config{}, err
	}
	return value, nil
}

func (c Config) validate() error {
	if c.ProtocolVersion != CurrentProtocolVersion {
		return errors.New("runner bootstrap protocol version mismatch")
	}
	if c.SandboxID == "" {
		return errors.New("runner bootstrap sandbox_id is required")
	}
	if c.Identity.ExecutionUID == 0 || c.Identity.ExecutionGID == 0 {
		return errors.New("runner bootstrap execution identity must be non-root")
	}
	if c.Identity.ExecutionUID == c.Identity.SocketOwnerUID || c.Identity.ExecutionGID == c.Identity.SocketOwnerGID {
		return errors.New("runner bootstrap execution identity must differ from socket owner")
	}
	if c.Paths.WorkspaceDirectory == "" || c.Paths.RuntimeDirectory == "" || c.Paths.SocketPath == "" || c.Paths.ExecutionDataDirectory == "" {
		return errors.New("runner bootstrap paths are required")
	}
	if c.Limits.DefaultTimeoutNanoseconds <= 0 || c.Limits.MaxTimeoutNanoseconds <= 0 || c.Limits.TerminationGraceNanoseconds <= 0 || c.Limits.MaxConcurrentExecutions <= 0 || c.Limits.MaxRequestBytes <= 0 || c.Limits.MaxOutputBytes <= 0 || c.Limits.MaxEnvVars <= 0 || c.Limits.MaxEnvKeyBytes <= 0 || c.Limits.MaxEnvValueBytes <= 0 || c.Limits.MaxEnvTotalBytes <= 0 || c.Limits.MaxLogPageEvents <= 0 || c.Limits.MaxLogPageBytes <= 0 || c.Limits.CompletedRetentionNanoseconds <= 0 || c.Limits.MaxRetainedExecutions <= 0 || c.Limits.SSEWriteTimeoutNanoseconds <= 0 {
		return errors.New("runner bootstrap limits are required")
	}
	return nil
}

type wireConfig struct {
	ProtocolVersion *int          `json:"protocol_version"`
	SandboxID       *string       `json:"sandbox_id"`
	Identity        *wireIdentity `json:"identity"`
	Limits          *wireLimits   `json:"limits"`
	Paths           *wirePaths    `json:"paths"`
	Features        *wireFeatures `json:"features"`
}

type wireFeatures struct {
	FilesEnabled                 *bool          `json:"files_enabled"`
	MaxUploadBytes               *int64         `json:"max_upload_bytes"`
	MaxDownloadBytes             *int64         `json:"max_download_bytes"`
	PTYEnabled                   *bool          `json:"pty_enabled"`
	PTYMaxConcurrentSessions     *int           `json:"pty_max_concurrent_sessions"`
	PTYDefaultTimeoutNanoseconds *time.Duration `json:"pty_default_timeout_nanoseconds"`
	PortProxyEnabled             *bool          `json:"port_proxy_enabled"`
	PortProxyMinPort             *int           `json:"port_proxy_min_port"`
	PortProxyMaxPort             *int           `json:"port_proxy_max_port"`
}

type wireIdentity struct {
	ExecutionUID   *uint32 `json:"execution_uid"`
	ExecutionGID   *uint32 `json:"execution_gid"`
	SocketOwnerUID *uint32 `json:"socket_owner_uid"`
	SocketOwnerGID *uint32 `json:"socket_owner_gid"`
}

type wireLimits struct {
	DefaultTimeoutNanoseconds     *time.Duration `json:"default_timeout_nanoseconds"`
	MaxTimeoutNanoseconds         *time.Duration `json:"max_timeout_nanoseconds"`
	TerminationGraceNanoseconds   *time.Duration `json:"termination_grace_nanoseconds"`
	MaxConcurrentExecutions       *int           `json:"max_concurrent_executions"`
	MaxRequestBytes               *int64         `json:"max_request_bytes"`
	MaxOutputBytes                *int64         `json:"max_output_bytes"`
	MaxEnvVars                    *int           `json:"max_env_vars"`
	MaxEnvKeyBytes                *int           `json:"max_env_key_bytes"`
	MaxEnvValueBytes              *int           `json:"max_env_value_bytes"`
	MaxEnvTotalBytes              *int64         `json:"max_env_total_bytes"`
	MaxLogPageEvents              *int           `json:"max_log_page_events"`
	MaxLogPageBytes               *int64         `json:"max_log_page_bytes"`
	CompletedRetentionNanoseconds *time.Duration `json:"completed_retention_nanoseconds"`
	MaxRetainedExecutions         *int           `json:"max_retained_executions"`
	SSEWriteTimeoutNanoseconds    *time.Duration `json:"sse_write_timeout_nanoseconds"`
}

type wirePaths struct {
	WorkspaceDirectory     *string `json:"workspace_directory"`
	RuntimeDirectory       *string `json:"runtime_directory"`
	SocketPath             *string `json:"socket_path"`
	ExecutionDataDirectory *string `json:"execution_data_directory"`
}

func (w wireConfig) value() (Config, error) {
	if w.ProtocolVersion == nil || w.SandboxID == nil || w.Identity == nil || w.Limits == nil || w.Paths == nil || w.Features == nil {
		return Config{}, errors.New("decode runner bootstrap: required top-level field is missing")
	}
	f := w.Features
	if f.FilesEnabled == nil || f.MaxUploadBytes == nil || f.MaxDownloadBytes == nil ||
		f.PTYEnabled == nil || f.PTYMaxConcurrentSessions == nil || f.PTYDefaultTimeoutNanoseconds == nil ||
		f.PortProxyEnabled == nil || f.PortProxyMinPort == nil || f.PortProxyMaxPort == nil {
		return Config{}, errors.New("decode runner bootstrap: required features field is missing")
	}
	i, l, p := w.Identity, w.Limits, w.Paths
	if i.ExecutionUID == nil || i.ExecutionGID == nil || i.SocketOwnerUID == nil || i.SocketOwnerGID == nil {
		return Config{}, errors.New("decode runner bootstrap: required identity field is missing")
	}
	if l.DefaultTimeoutNanoseconds == nil || l.MaxTimeoutNanoseconds == nil || l.TerminationGraceNanoseconds == nil || l.MaxConcurrentExecutions == nil || l.MaxRequestBytes == nil || l.MaxOutputBytes == nil || l.MaxEnvVars == nil || l.MaxEnvKeyBytes == nil || l.MaxEnvValueBytes == nil || l.MaxEnvTotalBytes == nil || l.MaxLogPageEvents == nil || l.MaxLogPageBytes == nil || l.CompletedRetentionNanoseconds == nil || l.MaxRetainedExecutions == nil || l.SSEWriteTimeoutNanoseconds == nil {
		return Config{}, errors.New("decode runner bootstrap: required limits field is missing")
	}
	if p.WorkspaceDirectory == nil || p.RuntimeDirectory == nil || p.SocketPath == nil || p.ExecutionDataDirectory == nil {
		return Config{}, errors.New("decode runner bootstrap: required paths field is missing")
	}
	return Config{
		ProtocolVersion: *w.ProtocolVersion,
		SandboxID:       *w.SandboxID,
		Identity:        Identity{ExecutionUID: *i.ExecutionUID, ExecutionGID: *i.ExecutionGID, SocketOwnerUID: *i.SocketOwnerUID, SocketOwnerGID: *i.SocketOwnerGID},
		Limits:          Limits{DefaultTimeoutNanoseconds: *l.DefaultTimeoutNanoseconds, MaxTimeoutNanoseconds: *l.MaxTimeoutNanoseconds, TerminationGraceNanoseconds: *l.TerminationGraceNanoseconds, MaxConcurrentExecutions: *l.MaxConcurrentExecutions, MaxRequestBytes: *l.MaxRequestBytes, MaxOutputBytes: *l.MaxOutputBytes, MaxEnvVars: *l.MaxEnvVars, MaxEnvKeyBytes: *l.MaxEnvKeyBytes, MaxEnvValueBytes: *l.MaxEnvValueBytes, MaxEnvTotalBytes: *l.MaxEnvTotalBytes, MaxLogPageEvents: *l.MaxLogPageEvents, MaxLogPageBytes: *l.MaxLogPageBytes, CompletedRetentionNanoseconds: *l.CompletedRetentionNanoseconds, MaxRetainedExecutions: *l.MaxRetainedExecutions, SSEWriteTimeoutNanoseconds: *l.SSEWriteTimeoutNanoseconds},
		Paths:           Paths{WorkspaceDirectory: *p.WorkspaceDirectory, RuntimeDirectory: *p.RuntimeDirectory, SocketPath: *p.SocketPath, ExecutionDataDirectory: *p.ExecutionDataDirectory},
		Features: Features{
			FilesEnabled:                 *f.FilesEnabled,
			MaxUploadBytes:               *f.MaxUploadBytes,
			MaxDownloadBytes:             *f.MaxDownloadBytes,
			PTYEnabled:                   *f.PTYEnabled,
			PTYMaxConcurrentSessions:     *f.PTYMaxConcurrentSessions,
			PTYDefaultTimeoutNanoseconds: *f.PTYDefaultTimeoutNanoseconds,
			PortProxyEnabled:             *f.PortProxyEnabled,
			PortProxyMinPort:             *f.PortProxyMinPort,
			PortProxyMaxPort:             *f.PortProxyMaxPort,
		},
	}, nil
}
