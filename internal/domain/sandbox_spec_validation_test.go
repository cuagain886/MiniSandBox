package domain

import (
	"errors"
	"strings"
	"testing"
)

// validSpec 返回满足当前阶段全部校验规则的 resolved spec 样例。
func validSpec() SandboxSpec {
	return SandboxSpec{
		Image: "alpine:3.22",
		Resources: ResourceLimits{
			CPUQuotaMillis: 500,
			MemoryMiB:      256,
			PIDs:           64,
		},
		Workspace: WorkspaceSpec{
			MountPath:  "/workspace",
			Persistent: false,
		},
		Network: NetworkSpec{
			Outbound: false,
		},
		Platform: Platform{
			OS:   "linux",
			Arch: "amd64",
		},
	}
}

// testBounds 返回测试使用的服务端资源上限。
func testBounds() ResourceBounds {
	return ResourceBounds{
		MaxCPUQuotaMillis: 2000,
		MaxMemoryMiB:      4096,
		MaxPIDs:           256,
	}
}

// TestSandboxSpecValidate 逐条验证当前阶段校验规则的拒绝行为。
func TestSandboxSpecValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SandboxSpec)
		field  string
	}{
		{
			name:   "empty image",
			mutate: func(s *SandboxSpec) { s.Image = "" },
			field:  "spec.image",
		},
		{
			name: "image too long",
			mutate: func(s *SandboxSpec) {
				s.Image = strings.Repeat("a", MaxImageReferenceLength+1)
			},
			field: "spec.image",
		},
		{
			name:   "cpu below range",
			mutate: func(s *SandboxSpec) { s.Resources.CPUQuotaMillis = 0 },
			field:  "spec.resources.cpu_quota_millis",
		},
		{
			name:   "cpu above range",
			mutate: func(s *SandboxSpec) { s.Resources.CPUQuotaMillis = 2001 },
			field:  "spec.resources.cpu_quota_millis",
		},
		{
			name:   "memory below range",
			mutate: func(s *SandboxSpec) { s.Resources.MemoryMiB = 0 },
			field:  "spec.resources.memory_mib",
		},
		{
			name:   "memory above range",
			mutate: func(s *SandboxSpec) { s.Resources.MemoryMiB = 4097 },
			field:  "spec.resources.memory_mib",
		},
		{
			name:   "pids below range",
			mutate: func(s *SandboxSpec) { s.Resources.PIDs = 0 },
			field:  "spec.resources.pids",
		},
		{
			name:   "pids above range",
			mutate: func(s *SandboxSpec) { s.Resources.PIDs = 257 },
			field:  "spec.resources.pids",
		},
		{
			name:   "mount path not workspace",
			mutate: func(s *SandboxSpec) { s.Workspace.MountPath = "/data" },
			field:  "spec.workspace.mount_path",
		},
		{
			name:   "persistent workspace",
			mutate: func(s *SandboxSpec) { s.Workspace.Persistent = true },
			field:  "spec.workspace.persistent",
		},
		{
			name:   "unsupported os",
			mutate: func(s *SandboxSpec) { s.Platform.OS = "windows" },
			field:  "spec.platform.os",
		},
		{
			name:   "unsupported arch",
			mutate: func(s *SandboxSpec) { s.Platform.Arch = "arm64" },
			field:  "spec.platform.arch",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := validSpec()
			test.mutate(&spec)

			err := spec.Validate(testBounds())
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error does not wrap ErrInvalid: %v", err)
			}
			var fieldErr *SpecFieldError
			if !errors.As(err, &fieldErr) {
				t.Fatalf("error is not a SpecFieldError: %v", err)
			}
			if got, want := fieldErr.Field, test.field; got != want {
				t.Fatalf("unexpected field: got %s, want %s", got, want)
			}
		})
	}
}

// TestSandboxSpecValidateAllowsOutboundContract 验证 Phase 2 resolved spec 可表达 outbound。
func TestSandboxSpecValidateAllowsOutboundContract(t *testing.T) {
	spec := validSpec()
	spec.Network.Outbound = true
	if err := spec.Validate(testBounds()); err != nil {
		t.Fatalf("validate outbound spec: %v", err)
	}
}

// TestSandboxSpecValidateAccepts 验证合法 spec 与边界取值可以通过校验。
func TestSandboxSpecValidateAccepts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SandboxSpec)
	}{
		{
			name:   "typical spec",
			mutate: func(*SandboxSpec) {},
		},
		{
			name: "image at maximum length",
			mutate: func(s *SandboxSpec) {
				s.Image = strings.Repeat("a", MaxImageReferenceLength)
			},
		},
		{
			name: "resources at server maximum",
			mutate: func(s *SandboxSpec) {
				s.Resources = ResourceLimits{
					CPUQuotaMillis: 2000,
					MemoryMiB:      4096,
					PIDs:           256,
				}
			},
		},
		{
			name: "resources at minimum",
			mutate: func(s *SandboxSpec) {
				s.Resources = ResourceLimits{
					CPUQuotaMillis: 1,
					MemoryMiB:      1,
					PIDs:           1,
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := validSpec()
			test.mutate(&spec)

			if err := spec.Validate(testBounds()); err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

// TestSandboxSpecValidateZeroBounds 验证零值 bounds 会拒绝而不是放行资源请求。
func TestSandboxSpecValidateZeroBounds(t *testing.T) {
	err := validSpec().Validate(ResourceBounds{})
	if err == nil {
		t.Fatal("expected validation error for zero bounds")
	}
	var fieldErr *SpecFieldError
	if !errors.As(err, &fieldErr) {
		t.Fatalf("error is not a SpecFieldError: %v", err)
	}
	if got, want := fieldErr.Field, "spec.resources.cpu_quota_millis"; got != want {
		t.Fatalf("unexpected field: got %s, want %s", got, want)
	}
}

// TestSandboxSpecValidateDoesNotEchoValues 验证错误消息不回显潜在秘密值。
func TestSandboxSpecValidateDoesNotEchoValues(t *testing.T) {
	const secret = "supersecret-token"

	spec := validSpec()
	spec.Image = "registry.example.com/team/app:" + secret +
		strings.Repeat("a", MaxImageReferenceLength)

	err := spec.Validate(testBounds())
	if err == nil {
		t.Fatal("expected validation error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error message echoes field value: %v", err)
	}

	spec = validSpec()
	spec.Workspace.MountPath = "/" + secret
	err = spec.Validate(testBounds())
	if err == nil {
		t.Fatal("expected validation error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error message echoes field value: %v", err)
	}
}
