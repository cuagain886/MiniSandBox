package docker

import (
	"context"
	"errors"
	"fmt"

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
) (actualResult runtimeport.ActualSandbox, resultErr error) {
	defer func() { r.observeDocker("ensure_sandbox", resultErr) }()
	names, err := r.validateEnsureInput(sandbox)
	if err != nil {
		return runtimeport.ActualSandbox{}, err
	}
	var readyEgress *runtimeport.EgressActual
	if sandbox.Spec.Network.Outbound {
		request, err := r.egressRequest(sandbox.ID)
		if err != nil {
			return runtimeport.ActualSandbox{}, err
		}
		egressActual, err := r.EnsureEgress(ctx, request)
		if err != nil {
			return runtimeport.ActualSandbox{}, err
		}
		readyEgress = &egressActual
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
			if err := r.validateMainContainerNetwork(ctx, actual.RuntimeID, sandbox.Spec.Network.Outbound, readyEgress); err != nil {
				return runtimeport.ActualSandbox{}, err
			}
			return actual, nil
		}
	}

	journal := ensureJournal{sandboxID: sandbox.ID}
	paths, err := EnsureRuntimeDirectory(r.dataDirectory, sandbox.ID)
	journal.directoryCreated = paths.CreatedByThisCall
	if err != nil {
		return runtimeport.ActualSandbox{}, ensureFailure(ctx, r, journal, err)
	}
	// 名称计算和目录创建必须指向同一个受管路径，避免未来改动造成 bind
	// mount 与 runner probe 使用不同目录。
	if paths.Directory != names.RuntimeDirectory ||
		paths.HostRunnerSocket != names.HostRunnerSocket {
		return runtimeport.ActualSandbox{}, ensureFailure(
			ctx,
			r,
			journal,
			errors.New("runtime directory identity is inconsistent"),
		)
	}
	if _, err := ensureImage(
		ctx,
		r.engine,
		sandbox.Spec.Image,
		r.createTimeout,
		r.imagePullLimiter,
	); err != nil {
		return runtimeport.ActualSandbox{}, ensureFailure(ctx, r, journal, err)
	}
	var volume WorkspaceVolumeResult
	if sandbox.ExpiresAt != nil {
		volume, err = ensureWorkspaceVolume(ctx, r.engine, sandbox.ID, sandbox.SpecHash, *sandbox.ExpiresAt)
	} else {
		volume, err = ensureWorkspaceVolume(ctx, r.engine, sandbox.ID, sandbox.SpecHash)
	}
	journal.volumeCreated = volume.CreatedByThisCall
	if err != nil {
		return runtimeport.ActualSandbox{}, ensureFailure(ctx, r, journal, err)
	}
	container, err := ensureStoppedContainerWithEgress(ctx, r.engine, sandbox, names, readyEgress)
	journal.containerCreated = container.CreatedByThisCall
	if err != nil {
		return runtimeport.ActualSandbox{}, ensureFailure(ctx, r, journal, err)
	}
	if err := copyArtifacts(
		ctx,
		r.engine,
		container.ContainerID,
		r.artifacts,
		r.createTimeout,
	); err != nil {
		return runtimeport.ActualSandbox{}, ensureFailure(ctx, r, journal, err)
	}
	// Docker Desktop/WSL 会在创建容器时才建立 bind source 映射；在此之后发布
	// 一次性材料，既保证首次启动可见，也缩短 token 在宿主机上的静态暴露窗口。
	if r.bootstrap != nil {
		if err := r.bootstrap.Stage(paths.Directory, sandbox.ID); err != nil {
			return runtimeport.ActualSandbox{}, ensureFailure(ctx, r, journal, fmt.Errorf("stage runner bootstrap failed: %w", err))
		}
	}
	if err := startContainer(ctx, r.engine, container.ContainerID); err != nil {
		return runtimeport.ActualSandbox{}, ensureFailure(ctx, r, journal, err)
	}
	actual, err = r.Inspect(ctx, sandbox.ID)
	if err != nil {
		return runtimeport.ActualSandbox{}, ensureFailure(ctx, r, journal, err)
	}
	if actual.State == runtimeport.ActualRunning {
		if err := r.validateMainContainerNetwork(ctx, actual.RuntimeID, sandbox.Spec.Network.Outbound, readyEgress); err != nil {
			return runtimeport.ActualSandbox{}, ensureFailure(ctx, r, journal, err)
		}
	}
	return actual, nil
}

// validateEnsureInput 在任何 Docker 或文件系统副作用前校验规格与 artifacts。
func (r *Runtime) validateEnsureInput(
	sandbox domain.Sandbox,
) (ResourceNames, error) {
	if r == nil || r.engine == nil {
		return ResourceNames{}, errors.New("docker runtime is not initialized")
	}
	options := RuntimeOptions{
		DataDirectory: r.dataDirectory,
		Artifacts:     r.artifacts,
		CreateTimeout: r.createTimeout,
		Bootstrap:     r.bootstrap,
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
	validationSandbox := sandbox
	if sandbox.Spec.Network.Outbound {
		if r.egressConfig == nil {
			return ResourceNames{}, errors.New("outbound sandbox is disabled by runtime configuration")
		}
		validationSandbox.Spec.Network.Outbound = false
	}
	if _, err := buildContainerCreateOptions(validationSandbox, names); err != nil {
		return ResourceNames{}, err
	}
	if _, err := ParseImageReference(sandbox.Spec.Image); err != nil {
		return ResourceNames{}, err
	}
	if err := validateArtifactSet(r.artifacts.Artifacts()); err != nil {
		return ResourceNames{}, &ArtifactInvalidError{cause: err}
	}
	return names, nil
}

func (r *Runtime) egressRequest(sandboxID string) (runtimeport.EgressRequest, error) {
	if r.egressConfig == nil {
		return runtimeport.EgressRequest{}, errors.New("outbound sandbox is disabled by runtime configuration")
	}
	return runtimeport.EgressRequest{
		SandboxID: sandboxID, Image: r.egressConfig.Image,
		AdditionalDeniedCIDRs: append([]string(nil), r.egressConfig.AdditionalDeniedCIDRs...),
		AnchorUID:             r.egressConfig.AnchorUID, AnchorGID: r.egressConfig.AnchorGID,
		Limits: r.egressConfig.Limits, ReadyTimeout: r.egressConfig.ReadyTimeout,
	}, nil
}
