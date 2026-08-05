package docker

import (
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	mobymount "github.com/moby/moby/api/types/mount"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/domain"
	"minisandbox/internal/runnerauth"
	"minisandbox/internal/runnerbootstrap"
)

// TestBuildContainerCreateOptionsAppliesSecurityBoundary 逐项验证 Phase 1 固定安全配置。
func TestBuildContainerCreateOptionsAppliesSecurityBoundary(t *testing.T) {
	names := testResourceNames(t)
	sandbox := testDockerSandbox()

	options, err := buildContainerCreateOptions(sandbox, names)
	if err != nil {
		t.Fatalf("build container options: %v", err)
	}
	if options.Name != names.Container {
		t.Fatalf("container name: got %q, want %q", options.Name, names.Container)
	}
	if options.Config == nil || options.HostConfig == nil || options.Platform == nil {
		t.Fatalf("required config missing: %#v", options)
	}
	config := options.Config
	if config.Image != sandbox.Spec.Image ||
		config.User != "0:0" ||
		config.WorkingDir != domain.WorkspaceMountPath {
		t.Fatalf("portable config: %#v", config)
	}
	if !reflect.DeepEqual(config.Entrypoint, fixedEntrypoint) || config.Cmd != nil {
		t.Fatalf("entrypoint/cmd: %#v %#v", config.Entrypoint, config.Cmd)
	}
	if !config.NetworkDisabled ||
		config.ExposedPorts != nil ||
		config.Env != nil ||
		config.Volumes != nil {
		t.Fatalf("unexpected portable capability: %#v", config)
	}
	if !reflect.DeepEqual(config.Labels, validTestLabels(t)) {
		t.Fatalf("labels: %#v", config.Labels)
	}

	host := options.HostConfig
	if host.Privileged ||
		host.NetworkMode != "none" ||
		host.PublishAllPorts ||
		host.PortBindings != nil ||
		host.Binds != nil ||
		host.ReadonlyRootfs {
		t.Fatalf("unsafe host config: %#v", host)
	}
	if !reflect.DeepEqual(host.CapDrop, []string{"ALL"}) ||
		!reflect.DeepEqual(
			host.CapAdd,
			[]string{"CHOWN", "SETUID", "SETGID", "KILL"},
		) ||
		!reflect.DeepEqual(
			host.SecurityOpt,
			[]string{noNewPrivilegesSecurity},
		) {
		t.Fatalf(
			"capabilities/security options: drop=%v add=%v security=%v",
			host.CapDrop,
			host.CapAdd,
			host.SecurityOpt,
		)
	}
	if len(host.Mounts) != 2 {
		t.Fatalf("mount count: got %d, want 2", len(host.Mounts))
	}
	assertMount(
		t,
		host.Mounts[0],
		mobymount.TypeVolume,
		names.Workspace,
		domain.WorkspaceMountPath,
	)
	assertMount(
		t,
		host.Mounts[1],
		mobymount.TypeBind,
		names.RuntimeDirectory,
		runnerbootstrap.RuntimeDirectory,
	)
	assertRunnerCredentialIsNotInContainerMetadata(t, options)
	if host.NanoCPUs != 500_000_000 ||
		host.Memory != 512*1024*1024 ||
		host.PidsLimit == nil ||
		*host.PidsLimit != 128 {
		t.Fatalf("resource conversion: %#v", host.Resources)
	}
	if options.Platform.OS != "linux" || options.Platform.Architecture != "amd64" {
		t.Fatalf("platform: %#v", options.Platform)
	}
	if options.NetworkingConfig != nil || options.Image != "" {
		t.Fatalf("unexpected create shortcut config: %#v", options)
	}
}

// assertRunnerCredentialIsNotInContainerMetadata 验证 runner 凭据只能通过受管运行时目录中的
// 一次性文件传递，不会进入可被 inspect 或进程参数读取的环境变量、命令和 labels。
func assertRunnerCredentialIsNotInContainerMetadata(
	t *testing.T,
	options mobyclient.ContainerCreateOptions,
) {
	t.Helper()
	const credentialCanary = "runner-credential-secret-canary"
	config := options.Config
	metadata := append([]string(nil), config.Env...)
	metadata = append(metadata, config.Entrypoint...)
	metadata = append(metadata, config.Cmd...)
	for key, value := range config.Labels {
		metadata = append(metadata, key, value)
	}
	joined := strings.Join(metadata, "\x00")
	if strings.Contains(joined, credentialCanary) ||
		strings.Contains(joined, runnerauth.CredentialFileName) {
		t.Fatalf("runner credential leaked into container metadata: %q", joined)
	}
	credentialPath := filepath.Join(
		runnerbootstrap.RuntimeDirectory,
		runnerauth.CredentialFileName,
	)
	for _, mount := range options.HostConfig.Mounts {
		if mount.Target == credentialPath {
			t.Fatalf("credential received a dedicated Docker mount: %#v", mount)
		}
	}
}

// TestBuildContainerCreateOptionsRejectsInjectedIdentity 验证调用方不能替换受管资源名称或 bind 路径。
func TestBuildContainerCreateOptionsRejectsInjectedIdentity(t *testing.T) {
	sandbox := testDockerSandbox()
	valid := testResourceNames(t)
	tests := []struct {
		name  string
		alter func(ResourceNames) ResourceNames
	}{
		{
			name: "container name",
			alter: func(names ResourceNames) ResourceNames {
				names.Container = "attacker"
				return names
			},
		},
		{
			name: "workspace name",
			alter: func(names ResourceNames) ResourceNames {
				names.Workspace = "attacker"
				return names
			},
		},
		{
			name: "relative runtime path",
			alter: func(names ResourceNames) ResourceNames {
				names.RuntimeDirectory = filepath.Join("relative", "run")
				return names
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := buildContainerCreateOptions(
				sandbox,
				tt.alter(valid),
			); err == nil {
				t.Fatal("injected identity was accepted")
			}
		})
	}
}

// TestConvertResourceLimitsRejectsUnlimitedOrOverflow 验证错误资源值不会变成 Docker unlimited。
func TestConvertResourceLimitsRejectsUnlimitedOrOverflow(t *testing.T) {
	tests := []domain.ResourceLimits{
		{CPUQuotaMillis: 0, MemoryMiB: 1, PIDs: 1},
		{CPUQuotaMillis: math.MaxInt64, MemoryMiB: 1, PIDs: 1},
		{CPUQuotaMillis: 1, MemoryMiB: 0, PIDs: 1},
		{CPUQuotaMillis: 1, MemoryMiB: math.MaxInt64, PIDs: 1},
		{CPUQuotaMillis: 1, MemoryMiB: 1, PIDs: 0},
	}
	for _, limits := range tests {
		if _, err := convertResourceLimits(limits); err == nil {
			t.Fatalf("invalid resource limits accepted: %#v", limits)
		}
	}
}

// assertMount 验证固定挂载的类型、源、目标和读写语义。
func assertMount(
	t *testing.T,
	actual mobymount.Mount,
	mountType mobymount.Type,
	source string,
	target string,
) {
	t.Helper()
	if actual.Type != mountType ||
		actual.Source != source ||
		actual.Target != target ||
		actual.ReadOnly {
		t.Fatalf("mount: %#v", actual)
	}
}

// testDockerSandbox 返回 Docker 原子测试共用的合法 resolved sandbox。
func testDockerSandbox() domain.Sandbox {
	return domain.Sandbox{
		ID:       testSandboxID,
		SpecHash: testSpecHash,
		Spec: domain.SandboxSpec{
			Image: "docker.io/library/debian:bookworm-slim",
			Resources: domain.ResourceLimits{
				CPUQuotaMillis: 500,
				MemoryMiB:      512,
				PIDs:           128,
			},
			Workspace: domain.WorkspaceSpec{
				MountPath: domain.WorkspaceMountPath,
			},
			Platform: domain.Platform{
				OS:   "linux",
				Arch: "amd64",
			},
		},
	}
}

// testResourceNames 返回与测试 sandbox ID 对应的完整确定性资源名。
func testResourceNames(t *testing.T) ResourceNames {
	t.Helper()
	names, err := NamesForSandbox(t.TempDir(), testSandboxID)
	if err != nil {
		t.Fatalf("build test resource names: %v", err)
	}
	return names
}
