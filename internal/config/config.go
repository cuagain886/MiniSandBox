// Package config 定义 sandboxd 的强类型配置模型、安全默认值、YAML 加载
// 与启动前安全校验。
//
// 本模块是配置字段和默认值的 source of truth,负责用 Go 类型表示 server、
// data、runtime、limits、runner 和 reconcile 配置,提供可直接生成完整 resolved
// spec 的默认值,从显式路径加载 YAML 覆盖默认值,并在启动前一次性拒绝
// 不安全或互相矛盾的配置。它不创建目录、不监听端口,也不连接外部依赖;
// 这些能力由后续里程碑实现。
package config

import (
	"time"

	"minisandbox/internal/domain"
)

// NetworkModeNone 表示 sandbox 完全没有网络访问能力,是 Phase 1 唯一
// 允许的网络模式。
const NetworkModeNone = "none"

// Config 汇总 sandboxd 的全部配置分组。
type Config struct {
	// Server 是控制面 HTTP 服务配置。
	Server ServerConfig
	// Data 是持久化数据的宿主机位置配置。
	Data DataConfig
	// Runtime 是 Docker runtime 与 sandbox 默认行为配置。
	Runtime RuntimeConfig
	// Limits 是 TTL 与资源的默认值和服务端上限。
	Limits LimitsConfig
	// Runner 是容器内执行身份、路径与有界资源限制配置。
	Runner RunnerConfig
	// Security 是内部鉴权材料路径与平台网络能力开关。
	Security SecurityConfig
	// Reconcile 是期望状态收敛的节奏与超时配置。
	Reconcile ReconcileConfig
}

// ServerConfig 描述控制面 HTTP 服务的监听与关闭行为。
type ServerConfig struct {
	// ListenAddress 是 HTTP 监听地址;Phase 1 只允许 loopback。
	ListenAddress string
	// ShutdownTimeout 是优雅关闭时等待存量请求完成的最长时间。
	ShutdownTimeout time.Duration
}

// DataConfig 描述控制面持久化数据的宿主机目录布局。
type DataConfig struct {
	// Directory 是受管数据根目录的绝对路径。
	Directory string
	// SQLitePath 是 SQLite 数据库文件的绝对路径。
	SQLitePath string
}

// RuntimeConfig 描述 Docker runtime 连接方式和 sandbox 的默认运行形态。
type RuntimeConfig struct {
	// Type 是 runtime 实现类型;Phase 1 只支持 docker。
	Type string
	// DockerHost 是 Docker Engine 的连接地址。
	DockerHost string
	// DefaultImage 是创建请求未指定镜像时使用的默认镜像引用。
	DefaultImage string
	// RunnerSocketDirectory 是各 sandbox runner socket 目录的宿主机根路径。
	RunnerSocketDirectory string
	// WorkspaceDirectory 是 workspace 数据的宿主机根路径。
	WorkspaceDirectory string
	// NetworkMode 是 sandbox 的网络模式;Phase 1 必须为 NetworkModeNone。
	NetworkMode string
	// WorkspacePersistent 表示删除 sandbox 后是否保留工作目录;
	// Phase 1 必须为 false。
	WorkspacePersistent bool
	// Platform 是 sandbox 容器与嵌入产物的目标平台;Phase 1 固定
	// linux/amd64。
	Platform domain.Platform
}

// LimitsConfig 描述 TTL 与资源的默认分配和服务端上限。
type LimitsConfig struct {
	// DefaultTTL 是创建请求未指定 TTL 时使用的默认存活时长。
	DefaultTTL time.Duration
	// MaximumTTL 是单个 sandbox 允许的最长存活时长。
	MaximumTTL time.Duration
	// DefaultResources 是未显式申请资源时分配给 sandbox 的默认上限。
	DefaultResources domain.ResourceLimits
	// MaxResources 是服务端允许申请的资源上限,供 spec 校验使用。
	MaxResources domain.ResourceBounds
}

// RunnerConfig 描述 runnerd 的固定执行身份、工作目录和服务端执行上限。
//
// 这些值由控制面提供，普通 execution 请求只能缩小 timeout 等可请求值，
// 不能扩大这里的上限。字节数使用 int64，条目数使用 int，避免单位含糊。
type RunnerConfig struct {
	// ExecutionUID 是 runnerd 永久降权后及其子进程使用的 Linux UID。
	ExecutionUID uint32
	// ExecutionGID 是 runnerd 永久降权后及其子进程使用的 Linux GID。
	ExecutionGID uint32
	// DefaultCWD 是请求未指定 cwd 时使用的容器内绝对工作目录。
	DefaultCWD string
	// DefaultTimeout 是请求未指定 timeout 时的执行超时。
	DefaultTimeout time.Duration
	// MaxTimeout 是单次 execution 可请求的最大执行时长。
	MaxTimeout time.Duration
	// TerminationGrace 是发送 TERM 后等待进程组退出再发送 KILL 的时长。
	TerminationGrace time.Duration
	// MaxConcurrentExecutions 是单个 sandbox 可同时运行的 execution 数量上限。
	MaxConcurrentExecutions int
	// MaxRequestBytes 是 runner HTTP 请求体允许的最大字节数。
	MaxRequestBytes int64
	// MaxOutputBytes 是单次 execution 的 stdout 与 stderr 合计保留上限。
	MaxOutputBytes int64
	// MaxEnvVars 是单次 execution 可提供的环境变量条目上限。
	MaxEnvVars int
	// MaxEnvKeyBytes 是单个环境变量名称的 UTF-8 字节上限。
	MaxEnvKeyBytes int
	// MaxEnvValueBytes 是单个环境变量值的 UTF-8 字节上限。
	MaxEnvValueBytes int
	// MaxEnvTotalBytes 是所有环境变量名称和值合计的 UTF-8 字节上限。
	MaxEnvTotalBytes int64
	// MaxLogPageEvents 是一次后台日志查询可返回的事件数上限。
	MaxLogPageEvents int
	// MaxLogPageBytes 是一次后台日志查询可返回的编码前数据字节上限。
	MaxLogPageBytes int64
	// CompletedRetention 是 terminal execution 至少保留供状态和日志查询的时长。
	CompletedRetention time.Duration
	// MaxRetainedExecutions 是单个 sandbox 可保留的 terminal execution 数量上限。
	MaxRetainedExecutions int
	// SSEWriteTimeout 是单次向客户端写出 SSE event 的最长等待时间。
	SSEWriteTimeout time.Duration
}

// SecurityConfig 描述不能由 sandbox 请求覆盖的平台安全配置。
type SecurityConfig struct {
	// RunnerMasterKeyFile 是 sandboxd 读取 runner 派生密钥所需主密钥的绝对路径。
	RunnerMasterKeyFile string
	// AllowOutbound 表示平台是否允许请求启用受管 outbound；默认关闭。
	AllowOutbound bool
}

// ReconcileConfig 描述期望状态收敛循环的节奏与阶段超时。
type ReconcileConfig struct {
	// Interval 是周期性扫描待收敛 sandbox 的间隔。
	Interval time.Duration
	// RunnerReadyTimeout 是容器启动后等待 runner 就绪的最长时间。
	RunnerReadyTimeout time.Duration
	// DeletionTimeout 是清理受管资源的单次收敛最长时间。
	DeletionTimeout time.Duration
}

// Default 返回 Phase 1 的安全默认配置。
//
// 默认值遵循安全边界:仅监听 loopback、网络模式为 none、workspace 非持久、
// CPU/内存/进程数有限且固定 linux/amd64。字段调整必须同步示例配置和文档。
func Default() Config {
	return Config{
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
			NetworkMode:           NetworkModeNone,
			WorkspacePersistent:   false,
			Platform: domain.Platform{
				OS:   "linux",
				Arch: "amd64",
			},
		},
		Limits: LimitsConfig{
			DefaultTTL: 30 * time.Minute,
			MaximumTTL: 24 * time.Hour,
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
		Reconcile: ReconcileConfig{
			Interval:           2 * time.Second,
			RunnerReadyTimeout: 30 * time.Second,
			DeletionTimeout:    30 * time.Second,
		},
	}
}

// DefaultSandboxSpec 用默认镜像和服务端默认值生成完整 resolved spec。
//
// 返回值覆盖 resolved spec 的全部字段,是后续"请求 + 默认值"合并的基础;
// 本方法不接受请求输入,也不生成 ID、hash 或持久化记录。
func (c Config) DefaultSandboxSpec() domain.SandboxSpec {
	return domain.SandboxSpec{
		Image:     c.Runtime.DefaultImage,
		Resources: c.Limits.DefaultResources,
		Workspace: domain.WorkspaceSpec{
			MountPath:  domain.WorkspaceMountPath,
			Persistent: c.Runtime.WorkspacePersistent,
		},
		Network: domain.NetworkSpec{
			Outbound: c.Runtime.NetworkMode != NetworkModeNone,
		},
		Platform: c.Runtime.Platform,
	}
}
