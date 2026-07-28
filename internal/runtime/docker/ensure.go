package docker

import (
	"context"
	"errors"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
)

// Ensure 按固定顺序幂等保证 sandbox 的 Docker 资源达到 running。
//
// 本方法只编排已审查的原子能力，不探测 runner、不写 Store。已经 running
// 且 spec identity 匹配的容器直接复用；created/stopped 容器会重新注入
// artifact 后再启动，使控制面在中途崩溃后可以从实际状态继续。
func (r *Runtime) Ensure(
	ctx context.Context,
	sandbox domain.Sandbox,
) (runtimeport.ActualSandbox, error) {
	names, err := r.validateEnsureInput(sandbox)
	if err != nil {
		return runtimeport.ActualSandbox{}, err
	}

	actual, err := r.Inspect(ctx, sandbox.ID)
	if err != nil {
		return runtimeport.ActualSandbox{}, err
	}
	if actual.State != runtimeport.ActualMissing {
		if actual.SpecHash != sandbox.SpecHash ||
			actual.Workspace != names.Workspace {
			return runtimeport.ActualSandbox{}, containerIdentityConflict()
		}
		if actual.State == runtimeport.ActualRunning {
			return actual, nil
		}
	}

	paths, err := EnsureRuntimeDirectory(r.dataDirectory, sandbox.ID)
	if err != nil {
		return runtimeport.ActualSandbox{}, err
	}
	// 名称计算和目录创建必须指向同一个受管路径，避免未来改动造成 bind
	// mount 与 runner probe 使用不同目录。
	if paths.Directory != names.RuntimeDirectory ||
		paths.HostRunnerSocket != names.HostRunnerSocket {
		return runtimeport.ActualSandbox{}, errors.New(
			"runtime directory identity is inconsistent",
		)
	}
	if _, err := ensureImage(
		ctx,
		r.engine,
		sandbox.Spec.Image,
		r.createTimeout,
	); err != nil {
		return runtimeport.ActualSandbox{}, err
	}
	if _, err := ensureWorkspaceVolume(
		ctx,
		r.engine,
		sandbox.ID,
		sandbox.SpecHash,
	); err != nil {
		return runtimeport.ActualSandbox{}, err
	}
	container, err := ensureStoppedContainer(ctx, r.engine, sandbox, names)
	if err != nil {
		return runtimeport.ActualSandbox{}, err
	}
	if err := copyArtifacts(
		ctx,
		r.engine,
		container.ContainerID,
		r.artifacts,
		r.createTimeout,
	); err != nil {
		return runtimeport.ActualSandbox{}, err
	}
	if err := startContainer(ctx, r.engine, container.ContainerID); err != nil {
		return runtimeport.ActualSandbox{}, err
	}
	return r.Inspect(ctx, sandbox.ID)
}

// validateEnsureInput 在任何 Docker 或文件系统副作用前校验规格与 artifacts。
func (r *Runtime) validateEnsureInput(
	sandbox domain.Sandbox,
) (ResourceNames, error) {
	if r == nil || r.engine == nil {
		return ResourceNames{}, errors.New("Docker runtime is not initialized")
	}
	options := RuntimeOptions{
		DataDirectory: r.dataDirectory,
		Artifacts:     r.artifacts,
		CreateTimeout: r.createTimeout,
	}
	if err := validateRuntimeOptions(options); err != nil {
		return ResourceNames{}, err
	}
	names, err := NamesForSandbox(r.dataDirectory, sandbox.ID)
	if err != nil {
		return ResourceNames{}, err
	}
	// buildContainerCreateOptions 同时校验完整 Phase 1 spec、spec hash、
	// labels 和资源单位，即使后续发现容器已 running 也不能跳过输入校验。
	if _, err := buildContainerCreateOptions(sandbox, names); err != nil {
		return ResourceNames{}, err
	}
	if _, err := ParseImageReference(sandbox.Spec.Image); err != nil {
		return ResourceNames{}, err
	}
	if err := validateArtifactSet(r.artifacts.Artifacts()); err != nil {
		return ResourceNames{}, err
	}
	return names, nil
}
