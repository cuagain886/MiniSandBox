// Package docker 承载基于 Docker Engine 的 sandbox runtime adapter。
//
// 本模块负责 Phase 1 的镜像、容器、workspace、labels、runner 注入和幂等
// 清理。它不负责租户鉴权、配额策略、reconcile 状态机或公共 API 错误码。
package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/domain"
	"minisandbox/internal/egressnft"
	"minisandbox/internal/egresspolicy"
	runtimeport "minisandbox/internal/runtime"
)

// Engine 是 Docker Runtime 当前实际使用的最小 Engine client 能力。
//
// 后续原子任务只在真正使用新 API 时扩展本接口，普通单元测试因此不需要
// Docker daemon，也不会依赖完整 SDK client。
type Engine interface {
	// Ping 探测 daemon，并通过 options 明确控制 API 版本协商。
	Ping(
		context.Context,
		mobyclient.PingOptions,
	) (mobyclient.PingResult, error)
	// ImageInspect 读取 daemon 中已有镜像的元数据。
	ImageInspect(
		context.Context,
		string,
		...mobyclient.ImageInspectOption,
	) (mobyclient.ImageInspectResult, error)
	// ImagePull 从 registry 拉取镜像并返回必须消费和关闭的响应流。
	ImagePull(
		context.Context,
		string,
		mobyclient.ImagePullOptions,
	) (mobyclient.ImagePullResponse, error)
	// ContainerInspect 读取确定性名称对应的容器状态和恢复元数据。
	ContainerInspect(
		context.Context,
		string,
		mobyclient.ContainerInspectOptions,
	) (mobyclient.ContainerInspectResult, error)
	// ContainerList 按受管 label 枚举 running 和 stopped 容器。
	ContainerList(
		context.Context,
		mobyclient.ContainerListOptions,
	) (mobyclient.ContainerListResult, error)
	// ContainerCreate 创建尚未启动的 sandbox 容器。
	ContainerCreate(
		context.Context,
		mobyclient.ContainerCreateOptions,
	) (mobyclient.ContainerCreateResult, error)
	// CopyToContainer 把固定 artifact tar 写入指定 stopped container。
	CopyToContainer(
		context.Context,
		string,
		mobyclient.CopyToContainerOptions,
	) (mobyclient.CopyToContainerResult, error)
	// ContainerStart 启动已经完成 artifact 注入的 stopped container。
	ContainerStart(
		context.Context,
		string,
		mobyclient.ContainerStartOptions,
	) (mobyclient.ContainerStartResult, error)
	// ContainerStop 优雅停止经过身份验证的 sandbox 容器。
	ContainerStop(
		context.Context,
		string,
		mobyclient.ContainerStopOptions,
	) (mobyclient.ContainerStopResult, error)
	// ContainerRemove 删除经过身份验证且已停止的 sandbox 容器。
	ContainerRemove(
		context.Context,
		string,
		mobyclient.ContainerRemoveOptions,
	) (mobyclient.ContainerRemoveResult, error)
	// VolumeInspect 读取确定性名称对应的 workspace volume 元数据。
	VolumeInspect(
		context.Context,
		string,
		mobyclient.VolumeInspectOptions,
	) (mobyclient.VolumeInspectResult, error)
	// VolumeCreate 创建带有恢复 labels 的 workspace named volume。
	VolumeCreate(
		context.Context,
		mobyclient.VolumeCreateOptions,
	) (mobyclient.VolumeCreateResult, error)
	// VolumeRemove 删除经过身份验证且未被容器占用的 workspace volume。
	VolumeRemove(
		context.Context,
		string,
		mobyclient.VolumeRemoveOptions,
	) (mobyclient.VolumeRemoveResult, error)
	// Close 释放 client 持有的连接与 transport 资源。
	Close() error
}

// RuntimeUnavailableError 表示 Docker daemon 当前不可访问。
//
// Error 使用固定安全文案；底层 cause 仅通过 errors.Is/As 保留给内部诊断，
// 公共 API mapper 通过 Unavailable marker 返回可重试 503。
type RuntimeUnavailableError struct {
	cause error
}

// Error 返回不包含 Docker host 或 socket 路径的固定文案。
func (e *RuntimeUnavailableError) Error() string {
	return "docker runtime unavailable"
}

// Unwrap 返回内部 cause，供控制面分类和诊断使用。
func (e *RuntimeUnavailableError) Unwrap() error {
	return e.cause
}

// Unavailable 标记该错误应映射为依赖不可用。
func (e *RuntimeUnavailableError) Unavailable() bool {
	return true
}

// FailureReason 返回稳定的 runtime unavailable 生命周期 reason。
func (e *RuntimeUnavailableError) FailureReason() string {
	return runtimeport.FailureReasonRuntimeUnavailable
}

// Runtime 是 runtime 端口的 Docker Engine 实现。
type Runtime struct {
	engine           Engine
	netNSResolver    NetNSResolver
	dataDirectory    string
	artifacts        ArtifactProvider
	createTimeout    time.Duration
	egressConfig     *EgressPlatformConfig
	bootstrap        RunnerBootstrapProvider
	bootstrapCloser  io.Closer
	imagePullLimiter runtimeport.Limiter
	egressLocksMu    sync.Mutex
	egressLocks      map[string]*egressAttachLock
}

// RunnerBootstrapProvider 为单个 sandbox 在受管 runtime 目录准备可信配置与一次性凭据。
type RunnerBootstrapProvider interface {
	// Stage 必须只写入给定受管目录，且不能把凭据放入 labels、环境变量或命令行。
	Stage(runtimeDirectory, sandboxID string) error
}

// EgressPlatformConfig 是只由 sandboxd 配置装配的 outbound sidecar 固定参数。
// 公共 sandbox 请求只能表达 outbound 布尔意图，不能覆盖本结构任何字段。
type EgressPlatformConfig struct {
	// Image 是已批准的精确 sidecar artifact digest。
	Image string
	// AdditionalDeniedCIDRs 是运维只增不减的额外拒绝网段。
	AdditionalDeniedCIDRs []string
	// AnchorUID 是 Ready anchor 的固定非 root UID。
	AnchorUID uint32
	// AnchorGID 是 Ready anchor 的固定非 root GID。
	AnchorGID uint32
	// Limits 是 sidecar 的固定资源上限。
	Limits domain.ResourceLimits
	// ReadyTimeout 是等待 attestation 的最长时间。
	ReadyTimeout time.Duration
}

// RuntimeOptions 保存 Docker Ensure 编排所需的宿主机受管依赖。
type RuntimeOptions struct {
	// DataDirectory 是已经由启动流程准备好的绝对数据根目录。
	DataDirectory string
	// Artifacts 提供经过平台校验的 runnerd 和 sandbox-init。
	Artifacts ArtifactProvider
	// CreateTimeout 限制镜像拉取和 artifact copy 的单步最长时间。
	CreateTimeout time.Duration
	// Egress 非 nil 时允许 runtime 为 outbound=true 创建受管 sidecar；nil 时 fail closed。
	Egress *EgressPlatformConfig
	// Bootstrap 负责在容器启动前重建 runner 可信配置和一次性 token。
	Bootstrap RunnerBootstrapProvider
	// ImagePullLimiter 是主容器与 egress sidecar 共享的实际拉取并发门禁；nil 表示不限制。
	ImagePullLimiter runtimeport.Limiter
}

// New 创建 Docker client 并立即探测 daemon。
//
// dockerHost 必须是显式配置值，不读取 DOCKER_HOST 环境变量。options 必须
// 提供受管 data directory、artifact provider 和正 create timeout。当前
// Moby client 默认启用 API version negotiation，不再传入已经弃用的空操作。
func New(
	ctx context.Context,
	dockerHost string,
	options RuntimeOptions,
) (*Runtime, error) {
	if err := validateRuntimeOptions(options); err != nil {
		return nil, err
	}
	engine, err := mobyclient.New(
		mobyclient.WithHost(dockerHost),
	)
	if err != nil {
		return nil, fmt.Errorf("create Docker client: %w", err)
	}
	runtime, err := newRuntime(ctx, engine)
	if err != nil {
		return nil, err
	}
	runtime.applyOptions(options)
	return runtime, nil
}

// newRuntime 使用可替换的窄 Engine 完成 Ping 和版本协商。
func newRuntime(ctx context.Context, engine Engine) (*Runtime, error) {
	if engine == nil {
		return nil, errors.New("docker engine must not be nil")
	}
	_, err := engine.Ping(
		ctx,
		mobyclient.PingOptions{NegotiateAPIVersion: true},
	)
	if err != nil {
		_ = engine.Close()
		return nil, &RuntimeUnavailableError{cause: err}
	}
	return &Runtime{engine: engine, netNSResolver: procNetNSResolver{}}, nil
}

// validateRuntimeOptions 在连接 Docker 前拒绝不完整的 Ensure 依赖。
func validateRuntimeOptions(options RuntimeOptions) error {
	if !filepath.IsAbs(options.DataDirectory) {
		return errors.New("runtime data directory must be absolute")
	}
	if options.Artifacts == nil {
		return errors.New("runtime artifact provider must not be nil")
	}
	if options.CreateTimeout <= 0 {
		return errors.New("runtime create timeout must be positive")
	}
	if options.Egress != nil {
		request := runtimeport.EgressRequest{
			SandboxID: "00000000-0000-4000-8000-000000000000", Image: options.Egress.Image,
			AdditionalDeniedCIDRs: options.Egress.AdditionalDeniedCIDRs,
			AnchorUID:             options.Egress.AnchorUID, AnchorGID: options.Egress.AnchorGID,
			Limits: options.Egress.Limits, ReadyTimeout: options.Egress.ReadyTimeout,
		}
		if !egressnft.ValidImageDigest(request.Image) || request.AnchorUID == 0 || request.AnchorGID == 0 ||
			request.ReadyTimeout <= 0 || request.ReadyTimeout > 2*time.Minute {
			return errors.New("runtime egress options are invalid")
		}
		if _, err := convertResourceLimits(request.Limits); err != nil {
			return errors.New("runtime egress resource limits are invalid")
		}
		if _, err := egresspolicy.Build(request.AdditionalDeniedCIDRs, nil); err != nil {
			return errors.New("runtime egress deny policy is invalid")
		}
	}
	return nil
}

// applyOptions 把已经校验的编排依赖保存到 Runtime。
func (r *Runtime) applyOptions(options RuntimeOptions) {
	r.dataDirectory = filepath.Clean(options.DataDirectory)
	r.artifacts = options.Artifacts
	r.createTimeout = options.CreateTimeout
	r.egressConfig = nil
	r.bootstrap = options.Bootstrap
	r.imagePullLimiter = options.ImagePullLimiter
	r.bootstrapCloser, _ = options.Bootstrap.(io.Closer)
	if options.Egress != nil {
		copy := *options.Egress
		copy.AdditionalDeniedCIDRs = append([]string(nil), options.Egress.AdditionalDeniedCIDRs...)
		r.egressConfig = &copy
	}
}

// Close 释放 Docker client 资源。
func (r *Runtime) Close() error {
	if r == nil || r.engine == nil {
		return nil
	}
	var bootstrapErr error
	if r.bootstrapCloser != nil {
		bootstrapErr = r.bootstrapCloser.Close()
	}
	return errors.Join(r.engine.Close(), bootstrapErr)
}

var _ runtimeport.Runtime = (*Runtime)(nil)
