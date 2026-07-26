package domain

import (
	"reflect"
	"regexp"
	"testing"
)

// TestSandboxSpecHashGolden 把规范化编码钉死在已知样例的 SHA-256 上。
//
// 该值被持久化记录与 Docker label 中的 spec-hash 比较依赖;若本测试失败,
// 说明规范化编码发生变化,必须先评估已存量 hash 的兼容性,而不是更新期望值。
func TestSandboxSpecHashGolden(t *testing.T) {
	const want = "b20b6f7fc65e9af3dbf497337c21003a0e65f3a5b206a0b10247f2958ab1fb2e"
	if got := validSpec().Hash(); got != want {
		t.Fatalf("canonical encoding changed: got %s, want %s", got, want)
	}
}

// TestSandboxSpecHashFormat 验证输出为 64 位小写十六进制 SHA-256。
func TestSandboxSpecHashFormat(t *testing.T) {
	format := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if got := validSpec().Hash(); !format.MatchString(got) {
		t.Fatalf("hash is not 64-char lowercase hex: %s", got)
	}
}

// TestSandboxSpecHashDeterministic 验证同一 spec 重复计算结果始终一致。
//
// Phase 1 的 resolved spec 不含 map 字段,计划中的"map 顺序不影响"要求由
// 固定字段顺序的规范化结构与本确定性测试共同覆盖;将来 spec 引入 map
// 字段时必须补充专门的顺序无关用例。
func TestSandboxSpecHashDeterministic(t *testing.T) {
	first := validSpec().Hash()
	for i := 0; i < 100; i++ {
		if got := validSpec().Hash(); got != first {
			t.Fatalf("hash changed across computations: got %s, want %s", got, first)
		}
	}

	independent := SandboxSpec{
		Image: "alpine:3.22",
		Resources: ResourceLimits{
			CPUQuotaMillis: 500,
			MemoryMiB:      256,
			PIDs:           64,
		},
		Workspace: WorkspaceSpec{MountPath: "/workspace"},
		Network:   NetworkSpec{},
		Platform:  Platform{OS: "linux", Arch: "amd64"},
	}
	if got := independent.Hash(); got != first {
		t.Fatalf("equal specs produced different hashes: got %s, want %s", got, first)
	}
}

// TestSandboxSpecHashFieldChanges 验证任意字段变化都会改变 hash。
func TestSandboxSpecHashFieldChanges(t *testing.T) {
	baseline := validSpec().Hash()

	tests := []struct {
		name   string
		mutate func(*SandboxSpec)
	}{
		{
			name:   "image",
			mutate: func(s *SandboxSpec) { s.Image = "busybox:1.37" },
		},
		{
			name:   "cpu quota",
			mutate: func(s *SandboxSpec) { s.Resources.CPUQuotaMillis = 501 },
		},
		{
			name:   "memory",
			mutate: func(s *SandboxSpec) { s.Resources.MemoryMiB = 257 },
		},
		{
			name:   "pids",
			mutate: func(s *SandboxSpec) { s.Resources.PIDs = 65 },
		},
		{
			name:   "mount path",
			mutate: func(s *SandboxSpec) { s.Workspace.MountPath = "/data" },
		},
		{
			name:   "persistent",
			mutate: func(s *SandboxSpec) { s.Workspace.Persistent = true },
		},
		{
			name:   "outbound",
			mutate: func(s *SandboxSpec) { s.Network.Outbound = true },
		},
		{
			name:   "os",
			mutate: func(s *SandboxSpec) { s.Platform.OS = "freebsd" },
		},
		{
			name:   "arch",
			mutate: func(s *SandboxSpec) { s.Platform.Arch = "arm64" },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := validSpec()
			test.mutate(&spec)
			if got := spec.Hash(); got == baseline {
				t.Fatalf("hash did not change for mutated field %s", test.name)
			}
		})
	}
}

// TestSandboxSpecHashCoversAllSpecFields 防止 SandboxSpec 新增字段而
// 规范化编码结构未同步扩展,导致字段悄悄逃出 hash 覆盖范围。
func TestSandboxSpecHashCoversAllSpecFields(t *testing.T) {
	tests := []struct {
		name      string
		spec      reflect.Type
		canonical reflect.Type
	}{
		{
			name:      "spec root",
			spec:      reflect.TypeOf(SandboxSpec{}),
			canonical: reflect.TypeOf(canonicalSpec{}),
		},
		{
			name:      "resources",
			spec:      reflect.TypeOf(ResourceLimits{}),
			canonical: reflect.TypeOf(canonicalResources{}),
		},
		{
			name:      "workspace",
			spec:      reflect.TypeOf(WorkspaceSpec{}),
			canonical: reflect.TypeOf(canonicalWorkspace{}),
		},
		{
			name:      "network",
			spec:      reflect.TypeOf(NetworkSpec{}),
			canonical: reflect.TypeOf(canonicalNetwork{}),
		},
		{
			name:      "platform",
			spec:      reflect.TypeOf(Platform{}),
			canonical: reflect.TypeOf(canonicalPlatform{}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, want := test.canonical.NumField(), test.spec.NumField(); got != want {
				t.Fatalf(
					"canonical struct field count mismatch: got %d, want %d",
					got,
					want,
				)
			}
		})
	}
}
