package application

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"minisandbox/internal/config"
	"minisandbox/internal/domain"
)

// TestSandboxSpecBuilderUsesServerDefaults 验证 image/outbound 来自请求且其余字段来自配置。
func TestSandboxSpecBuilderUsesServerDefaults(t *testing.T) {
	cfg := config.Default()
	builder := NewSandboxSpecBuilder(
		cfg.DefaultSandboxSpec(),
		cfg.Limits.MaxResources,
	)

	got, err := builder.Build(CreateSandbox{Image: "alpine:3.22"})
	if err != nil {
		t.Fatalf("build spec: %v", err)
	}
	want := cfg.DefaultSandboxSpec()
	want.Image = "alpine:3.22"
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved spec mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

// TestSandboxSpecBuilderMapsOutbound 验证缺失/false 仍为 network none，true 改变 resolved spec。
func TestSandboxSpecBuilderMapsOutbound(t *testing.T) {
	cfg := config.Default()
	defaults := cfg.DefaultSandboxSpec()
	defaults.Network.Outbound = true
	builder := NewSandboxSpecBuilder(defaults, cfg.Limits.MaxResources)

	without, err := builder.Build(CreateSandbox{Image: "alpine:3.22"})
	if err != nil {
		t.Fatalf("build omitted outbound: %v", err)
	}
	if without.Network.Outbound {
		t.Fatal("omitted outbound must resolve to false")
	}
	with, err := builder.Build(CreateSandbox{Image: "alpine:3.22", Outbound: true})
	if err != nil {
		t.Fatalf("build outbound: %v", err)
	}
	if !with.Network.Outbound {
		t.Fatal("outbound=true was not mapped")
	}
	if without.Hash() == with.Hash() {
		t.Fatal("outbound change must alter spec hash")
	}
}

// TestSandboxSpecBuilderRejectsInvalidImage 验证请求 image 不会回退到配置默认值。
func TestSandboxSpecBuilderRejectsInvalidImage(t *testing.T) {
	cfg := config.Default()
	builder := NewSandboxSpecBuilder(
		cfg.DefaultSandboxSpec(),
		cfg.Limits.MaxResources,
	)

	for _, image := range []string{
		"",
		strings.Repeat("x", domain.MaxImageReferenceLength+1),
	} {
		_, err := builder.Build(CreateSandbox{Image: image})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("image length %d: got %v, want ErrInvalid", len(image), err)
		}
		var fieldErr *domain.SpecFieldError
		if !errors.As(err, &fieldErr) || fieldErr.Field != "spec.image" {
			t.Fatalf("image length %d: unexpected field error %v", len(image), err)
		}
		if image != "" && strings.Contains(err.Error(), image) {
			t.Fatal("validation error leaked invalid image value")
		}
	}
}

// TestSandboxSpecBuilderResourceBoundary 验证服务端上限边界由领域校验执行。
func TestSandboxSpecBuilderResourceBoundary(t *testing.T) {
	cfg := config.Default()
	defaults := cfg.DefaultSandboxSpec()
	defaults.Resources = domain.ResourceLimits{
		CPUQuotaMillis: cfg.Limits.MaxResources.MaxCPUQuotaMillis,
		MemoryMiB:      cfg.Limits.MaxResources.MaxMemoryMiB,
		PIDs:           cfg.Limits.MaxResources.MaxPIDs,
	}
	builder := NewSandboxSpecBuilder(defaults, cfg.Limits.MaxResources)
	if _, err := builder.Build(CreateSandbox{Image: "alpine:3.22"}); err != nil {
		t.Fatalf("build at resource maximum: %v", err)
	}

	defaults.Resources.MemoryMiB++
	builder = NewSandboxSpecBuilder(defaults, cfg.Limits.MaxResources)
	_, err := builder.Build(CreateSandbox{Image: "alpine:3.22"})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("build above resource maximum: got %v, want ErrInvalid", err)
	}
	var fieldErr *domain.SpecFieldError
	if !errors.As(err, &fieldErr) ||
		fieldErr.Field != "spec.resources.memory_mib" {
		t.Fatalf("unexpected resource field error: %v", err)
	}
}

// TestSandboxSpecBuilderDeterministic 验证重复构建不会修改默认值或内部状态。
func TestSandboxSpecBuilderDeterministic(t *testing.T) {
	cfg := config.Default()
	defaults := cfg.DefaultSandboxSpec()
	builder := NewSandboxSpecBuilder(defaults, cfg.Limits.MaxResources)
	command := CreateSandbox{Image: "busybox:1.36"}

	first, err := builder.Build(command)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	second, err := builder.Build(command)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated builds differ: %#v / %#v", first, second)
	}
	if !reflect.DeepEqual(defaults, cfg.DefaultSandboxSpec()) {
		t.Fatal("builder modified caller defaults")
	}
}

// TestCreateSandboxCommandSurface 验证应用命令只接收 image 和 outbound 意图。
func TestCreateSandboxCommandSurface(t *testing.T) {
	commandType := reflect.TypeOf(CreateSandbox{})
	if commandType.NumField() != 2 || commandType.Field(0).Name != "Image" ||
		commandType.Field(1).Name != "Outbound" {
		t.Fatalf("unexpected CreateSandbox fields: %#v", commandType)
	}
}
