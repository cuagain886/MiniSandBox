package docker

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	mobycontainer "github.com/moby/moby/api/types/container"
	mobymount "github.com/moby/moby/api/types/mount"
	mobyclient "github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"minisandbox/internal/domain"
	"minisandbox/internal/egressnft"
	"minisandbox/internal/runnerbootstrap"
	runtimeport "minisandbox/internal/runtime"
)

const (
	artifactDirectory       = "/opt/minisandbox"
	sandboxInitPath         = artifactDirectory + "/sandbox-init"
	runnerPath              = artifactDirectory + "/runnerd"
	noNewPrivilegesSecurity = "no-new-privileges:true"
)

var fixedEntrypoint = []string{
	sandboxInitPath,
	"--",
	runnerPath,
	"serve",
}

// ContainerEnsureResult 描述 stopped container 原子保证操作的结果。
type ContainerEnsureResult struct {
	// ContainerID 是 Docker daemon 分配的稳定容器 ID。
	ContainerID string
	// CreatedByThisCall 表示本次调用创建了容器，供后续失败补偿判断。
	CreatedByThisCall bool
}

// buildContainerCreateOptions 把 resolved sandbox 转为固定安全 Docker 配置。
//
// 调用方只能提供领域规格与由 naming helper 生成的资源名称，不能注入
// HostConfig、任意 bind mount、端口、网络或启动命令。
func buildContainerCreateOptions(
	sandbox domain.Sandbox,
	names ResourceNames,
) (mobyclient.ContainerCreateOptions, error) {
	return buildContainerCreateOptionsWithEgress(sandbox, names, nil)
}

func buildContainerCreateOptionsWithEgress(
	sandbox domain.Sandbox,
	names ResourceNames,
	egress *runtimeport.EgressActual,
) (mobyclient.ContainerCreateOptions, error) {
	if !validSandboxID(sandbox.ID) ||
		names.Container != containerName(sandbox.ID) ||
		names.Workspace != workspaceName(sandbox.ID) ||
		!filepath.IsAbs(names.RuntimeDirectory) {
		return mobyclient.ContainerCreateOptions{}, errors.New(
			"managed container identity is invalid",
		)
	}
	if sandbox.Spec.Workspace.MountPath != domain.WorkspaceMountPath ||
		sandbox.Spec.Workspace.Persistent ||
		sandbox.Spec.Platform.OS != "linux" ||
		sandbox.Spec.Platform.Arch != "amd64" {
		return mobyclient.ContainerCreateOptions{}, errors.New(
			"sandbox spec is incompatible with Phase 1 runtime",
		)
	}
	labels, err := EncodeLabels(ManagedLabels{
		SandboxID:             sandbox.ID,
		SpecHash:              sandbox.SpecHash,
		Workspace:             names.Workspace,
		RunnerProtocolVersion: runnerbootstrap.CurrentProtocolVersion,
	})
	if err != nil {
		return mobyclient.ContainerCreateOptions{}, err
	}
	resources, err := convertResourceLimits(sandbox.Spec.Resources)
	if err != nil {
		return mobyclient.ContainerCreateOptions{}, err
	}

	networkDisabled := true
	networkMode := mobycontainer.NetworkMode("none")
	if sandbox.Spec.Network.Outbound {
		if !validReadyEgress(egress) || egress.SandboxID != sandbox.ID {
			return mobyclient.ContainerCreateOptions{}, errors.New("outbound sandbox requires Ready egress sidecar")
		}
		networkDisabled = false
		networkMode = mobycontainer.NetworkMode("container:" + egress.ContainerID)
	} else if egress != nil {
		return mobyclient.ContainerCreateOptions{}, errors.New("network-none sandbox must not receive egress sidecar")
	}

	return mobyclient.ContainerCreateOptions{
		Name: names.Container,
		Config: &mobycontainer.Config{
			User:            "0:0",
			Image:           sandbox.Spec.Image,
			WorkingDir:      domain.WorkspaceMountPath,
			Entrypoint:      append([]string(nil), fixedEntrypoint...),
			NetworkDisabled: networkDisabled,
			Labels:          labels,
		},
		HostConfig: &mobycontainer.HostConfig{
			NetworkMode:    networkMode,
			Privileged:     false,
			CapDrop:        []string{"ALL"},
			CapAdd:         []string{"CHOWN", "SETUID", "SETGID", "KILL"},
			SecurityOpt:    []string{noNewPrivilegesSecurity},
			ReadonlyRootfs: false,
			Resources:      resources,
			Mounts: []mobymount.Mount{
				{
					Type:   mobymount.TypeVolume,
					Source: names.Workspace,
					Target: domain.WorkspaceMountPath,
				},
				{
					Type:   mobymount.TypeBind,
					Source: names.RuntimeDirectory,
					Target: runnerbootstrap.RuntimeDirectory,
				},
			},
		},
		Platform: &ocispec.Platform{
			OS:           "linux",
			Architecture: "amd64",
		},
	}, nil
}

func validReadyEgress(actual *runtimeport.EgressActual) bool {
	return actual != nil && actual.State == runtimeport.ActualRunning && actual.ContainerID != "" && actual.NetworkID != "" &&
		actual.Policy.Hash != "" && actual.Attestation.PolicyHash == actual.Policy.Hash &&
		actual.Attestation.ProtocolVersion == actual.Policy.ProtocolVersion &&
		actual.Attestation.RuleSchemaVersion == actual.Policy.RuleSchemaVersion &&
		egressnft.ValidNetworkNamespace(actual.Attestation.NetworkNamespace) && egressnft.ValidImageDigest(actual.Attestation.ImageDigest)
}

// ensureStoppedContainer 幂等创建或复用当前 sandbox 的受管容器。
//
// 本函数只建立容器对象，不复制 artifact、启动容器或执行容器内命令。
func ensureStoppedContainer(
	ctx context.Context,
	engine Engine,
	sandbox domain.Sandbox,
	names ResourceNames,
) (ContainerEnsureResult, error) {
	return ensureStoppedContainerWithEgress(ctx, engine, sandbox, names, nil)
}

func ensureStoppedContainerWithEgress(
	ctx context.Context,
	engine Engine,
	sandbox domain.Sandbox,
	names ResourceNames,
	egress *runtimeport.EgressActual,
) (ContainerEnsureResult, error) {
	options, err := buildContainerCreateOptionsWithEgress(sandbox, names, egress)
	if err != nil {
		return ContainerEnsureResult{}, err
	}
	expected := ManagedLabels{
		SandboxID:             sandbox.ID,
		SpecHash:              sandbox.SpecHash,
		Workspace:             names.Workspace,
		RunnerProtocolVersion: runnerbootstrap.CurrentProtocolVersion,
	}

	inspection, err := engine.ContainerInspect(
		ctx,
		names.Container,
		mobyclient.ContainerInspectOptions{},
	)
	switch {
	case err == nil:
		if err := validateManagedContainer(
			inspection.Container,
			names.Container,
			expected,
		); err != nil {
			return ContainerEnsureResult{}, err
		}
		return ContainerEnsureResult{
			ContainerID: inspection.Container.ID,
		}, nil
	case !cerrdefs.IsNotFound(err):
		return ContainerEnsureResult{}, runtimeUnavailable(err)
	}

	created, err := engine.ContainerCreate(ctx, options)
	if err != nil {
		if cerrdefs.IsConflict(err) {
			return ContainerEnsureResult{}, &SpecDriftError{cause: domain.ErrConflict}
		}
		return ContainerEnsureResult{}, &ContainerCreateFailedError{cause: err}
	}
	if created.ID == "" {
		return ContainerEnsureResult{CreatedByThisCall: true},
			&ContainerCreateFailedError{
				cause: errors.New("docker returned an empty container ID"),
			}
	}
	return ContainerEnsureResult{
		ContainerID:       created.ID,
		CreatedByThisCall: true,
	}, nil
}

// validateManagedContainer 验证 inspect 结果属于预期 sandbox 和规格。
func validateManagedContainer(
	container mobycontainer.InspectResponse,
	expectedName string,
	expected ManagedLabels,
) error {
	if container.ID == "" ||
		strings.TrimPrefix(container.Name, "/") != expectedName ||
		container.Config == nil {
		return containerIdentityConflict()
	}
	actual, err := ParseLabels(container.Config.Labels)
	if err != nil || !managedLabelIdentityEqual(actual, expected) {
		return containerIdentityConflict()
	}
	return nil
}

// containerIdentityConflict 返回不泄露 inspect 数据的稳定受管身份冲突。
func containerIdentityConflict() error {
	return &SpecDriftError{cause: domain.ErrConflict}
}
