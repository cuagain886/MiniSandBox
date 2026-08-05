package runner

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"minisandbox/internal/runnerbootstrap"
)

func testEnvironmentBuilder(t *testing.T) *EnvironmentBuilder {
	t.Helper()
	builder, err := NewEnvironmentBuilder(runnerbootstrap.Limits{
		MaxEnvVars:       8,
		MaxEnvKeyBytes:   32,
		MaxEnvValueBytes: 32,
		MaxEnvTotalBytes: 128,
	})
	if err != nil {
		t.Fatalf("new environment builder: %v", err)
	}
	return builder
}

// TestEnvironmentBuilderSanitizesMergesAndSorts 验证 image 重复项取最后值、request 覆盖且结果稳定排序。
func TestEnvironmentBuilderSanitizesMergesAndSorts(t *testing.T) {
	image := []string{"PATH=/usr/bin", "DUP=old", "MALFORMED", "9BAD=value", "DUP=image-last", "EMPTY="}
	request := map[string]string{"DUP": "request", "HOME": "/workspace", "ZED": "last"}
	want := []string{"DUP=request", "EMPTY=", "HOME=/workspace", "PATH=/usr/bin", "ZED=last"}
	got, err := testEnvironmentBuilder(t).Build(image, request)
	if err != nil {
		t.Fatalf("build environment: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment: got %v, want %v", got, want)
	}
	got[0] = "CHANGED=value"
	if image[0] != "PATH=/usr/bin" || request["DUP"] != "request" {
		t.Fatal("builder mutated caller input")
	}
}

// TestEnvironmentBuilderFiltersInternalKeysCaseInsensitively 验证内部前缀、token/socket/bootstrap 和固定 denylist 均不可覆盖。
func TestEnvironmentBuilderFiltersInternalKeysCaseInsensitively(t *testing.T) {
	const secret = "credential-secret-canary"
	image := []string{
		"MINISANDBOX_RUNNER_TOKEN=" + secret,
		"runner_socket=" + secret,
		"RUNNER_BOOTSTRAP_CONFIG=" + secret,
		"LD_PRELOAD=" + secret,
		"SAFE=image",
	}
	request := map[string]string{
		"minisandbox_custom": secret,
		"Runner_Token":       secret,
		"bash_env":           secret,
		"SAFE":               "request",
	}
	got, err := testEnvironmentBuilder(t).Build(image, request)
	if err != nil {
		t.Fatalf("build environment: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"SAFE=request"}) {
		t.Fatalf("filtered environment: %v", got)
	}
	if strings.Contains(strings.Join(got, "\x00"), secret) {
		t.Fatal("sensitive value survived filtering")
	}
}

// TestEnvironmentBuilderRejectsInvalidRequestWithoutValueLeak 验证非法 key/NUL 返回固定错误且不回显值。
func TestEnvironmentBuilderRejectsInvalidRequestWithoutValueLeak(t *testing.T) {
	const secret = "environment-secret-canary"
	requests := []map[string]string{
		{"": secret},
		{"9BAD": secret},
		{"BAD-KEY": secret},
		{"BAD=KEY": secret},
		{"GOOD": secret + "\x00tail"},
	}
	for _, request := range requests {
		got, err := testEnvironmentBuilder(t).Build(nil, request)
		if !errors.Is(err, ErrInvalidExecutionEnvironment) || got != nil {
			t.Fatalf("invalid request: got %v, err %v", got, err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatal("environment value leaked into error")
		}
	}
}

// TestEnvironmentBuilderEnforcesFinalLimits 覆盖条目数、key/value 和总字节边界。
func TestEnvironmentBuilderEnforcesFinalLimits(t *testing.T) {
	tests := []struct {
		name   string
		limits runnerbootstrap.Limits
		env    map[string]string
	}{
		{name: "count", limits: runnerbootstrap.Limits{MaxEnvVars: 1, MaxEnvKeyBytes: 8, MaxEnvValueBytes: 8, MaxEnvTotalBytes: 16}, env: map[string]string{"A": "1", "B": "2"}},
		{name: "key", limits: runnerbootstrap.Limits{MaxEnvVars: 2, MaxEnvKeyBytes: 1, MaxEnvValueBytes: 8, MaxEnvTotalBytes: 16}, env: map[string]string{"AB": "1"}},
		{name: "value", limits: runnerbootstrap.Limits{MaxEnvVars: 2, MaxEnvKeyBytes: 8, MaxEnvValueBytes: 1, MaxEnvTotalBytes: 16}, env: map[string]string{"A": "12"}},
		{name: "total", limits: runnerbootstrap.Limits{MaxEnvVars: 2, MaxEnvKeyBytes: 8, MaxEnvValueBytes: 8, MaxEnvTotalBytes: 3}, env: map[string]string{"A": "1", "B": "2"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			builder, err := NewEnvironmentBuilder(test.limits)
			if err != nil {
				t.Fatalf("new builder: %v", err)
			}
			if _, err := builder.Build(nil, test.env); !errors.Is(err, ErrInvalidExecutionEnvironment) {
				t.Fatalf("limit overflow: %v", err)
			}
		})
	}
}

// TestEnvironmentBuilderAppliesLimitsAfterOverride 验证被 request 覆盖的超长 image 值不会污染最终 limit 计算。
func TestEnvironmentBuilderAppliesLimitsAfterOverride(t *testing.T) {
	builder, err := NewEnvironmentBuilder(runnerbootstrap.Limits{MaxEnvVars: 1, MaxEnvKeyBytes: 4, MaxEnvValueBytes: 2, MaxEnvTotalBytes: 3})
	if err != nil {
		t.Fatalf("new builder: %v", err)
	}
	got, err := builder.Build([]string{"A=image-value-too-long"}, map[string]string{"A": "ok"})
	if err != nil || !reflect.DeepEqual(got, []string{"A=ok"}) {
		t.Fatalf("override: got %v, err %v", got, err)
	}
}

// TestNewEnvironmentBuilderRejectsInvalidLimits 验证无界环境配置不能创建 builder。
func TestNewEnvironmentBuilderRejectsInvalidLimits(t *testing.T) {
	if _, err := NewEnvironmentBuilder(runnerbootstrap.Limits{}); err == nil {
		t.Fatal("zero environment limits accepted")
	}
}
