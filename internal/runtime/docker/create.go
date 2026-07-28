package docker

import (
	"errors"
	"path/filepath"

	mobycontainer "github.com/moby/moby/api/types/container"
	mobymount "github.com/moby/moby/api/types/mount"
	mobyclient "github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"minisandbox/internal/domain"
)

const (
	artifactDirectory       = "/opt/minisandbox"
	sandboxInitPath         = artifactDirectory + "/sandbox-init"
	runnerPath              = artifactDirectory + "/runnerd"
	guestRuntimeDirectory   = "/run/minisandbox"
	noNewPrivilegesSecurity = "no-new-privileges:true"
)

var fixedEntrypoint = []string{
	sandboxInitPath,
	"--",
	runnerPath,
	"serve",
}

// buildContainerCreateOptions 把 resolved sandbox 转为固定安全 Docker 配置。
//
// 调用方只能提供领域规格与由 naming helper 生成的资源名称，不能注入
// HostConfig、任意 bind mount、端口、网络或启动命令。
func buildContainerCreateOptions(
	sandbox domain.Sandbox,
	names ResourceNames,
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
		sandbox.Spec.Network.Outbound ||
		sandbox.Spec.Platform.OS != "linux" ||
		sandbox.Spec.Platform.Arch != "amd64" {
		return mobyclient.ContainerCreateOptions{}, errors.New(
			"sandbox spec is incompatible with Phase 1 runtime",
		)
	}
	labels, err := EncodeLabels(ManagedLabels{
		SandboxID: sandbox.ID,
		SpecHash:  sandbox.SpecHash,
		Workspace: names.Workspace,
	})
	if err != nil {
		return mobyclient.ContainerCreateOptions{}, err
	}
	resources, err := convertResourceLimits(sandbox.Spec.Resources)
	if err != nil {
		return mobyclient.ContainerCreateOptions{}, err
	}

	return mobyclient.ContainerCreateOptions{
		Name: names.Container,
		Config: &mobycontainer.Config{
			User:            "0:0",
			Image:           sandbox.Spec.Image,
			WorkingDir:      domain.WorkspaceMountPath,
			Entrypoint:      append([]string(nil), fixedEntrypoint...),
			NetworkDisabled: true,
			Labels:          labels,
		},
		HostConfig: &mobycontainer.HostConfig{
			NetworkMode:    mobycontainer.NetworkMode("none"),
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
					Target: guestRuntimeDirectory,
				},
			},
		},
		Platform: &ocispec.Platform{
			OS:           "linux",
			Architecture: "amd64",
		},
	}, nil
}
