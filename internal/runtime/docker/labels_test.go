package docker

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/runnerbootstrap"
)

const (
	testSandboxID = "00010203-0405-4607-8809-0a0b0c0d0e0f"
	testSpecHash  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testWorkspace = "minisandbox-workspace-" + testSandboxID
)

// TestLabelsRoundTrip 验证固定恢复与 runner 版本契约可以无损编解码。
func TestLabelsRoundTrip(t *testing.T) {
	metadata := ManagedLabels{
		SchemaVersion:         2,
		SandboxID:             testSandboxID,
		SpecHash:              testSpecHash,
		Workspace:             testWorkspace,
		RunnerProtocolVersion: runnerbootstrap.CurrentProtocolVersion,
	}
	labels, err := EncodeLabels(metadata)
	if err != nil {
		t.Fatalf("encode labels: %v", err)
	}
	if len(labels) != len(managedLabelKeys) {
		t.Fatalf("label count: got %d, want %d", len(labels), len(managedLabelKeys))
	}
	if labels[LabelManaged] != "true" ||
		labels[LabelSchemaVersion] != "2" ||
		labels[LabelExpiresAt] != "" {
		t.Fatalf("fixed labels: %#v", labels)
	}

	labels["third-party.example/annotation"] = "allowed"
	parsed, err := ParseLabels(labels)
	if err != nil {
		t.Fatalf("parse labels: %v", err)
	}
	if !reflect.DeepEqual(parsed, metadata) {
		t.Fatalf("round trip: got %#v, want %#v", parsed, metadata)
	}
}

// TestParseLabelsAcceptsLegacyV1WithoutRewrite 验证双版本 reader 只解析旧资源而不修改输入。
func TestParseLabelsAcceptsLegacyV1WithoutRewrite(t *testing.T) {
	labels, err := EncodeLabels(ManagedLabels{
		SandboxID: testSandboxID, SpecHash: testSpecHash, Workspace: testWorkspace,
		RunnerProtocolVersion: runnerbootstrap.CurrentProtocolVersion,
	})
	if err != nil {
		t.Fatalf("encode labels: %v", err)
	}
	labels[LabelSchemaVersion] = labelSchemaVersionV1
	parsed, err := ParseLabels(labels)
	if err != nil || parsed.SchemaVersion != 1 || labels[LabelSchemaVersion] != labelSchemaVersionV1 {
		t.Fatalf("legacy parse: %#v/%v labels=%#v", parsed, err, labels)
	}
}

// TestParseLabelsRejectsMissingFields 验证每个恢复字段都不可省略。
func TestParseLabelsRejectsMissingFields(t *testing.T) {
	labels := validTestLabels(t)
	for _, missing := range managedLabelKeys {
		t.Run(missing, func(t *testing.T) {
			copy := cloneLabels(labels)
			delete(copy, missing)
			if _, err := ParseLabels(copy); err == nil {
				t.Fatalf("missing %s was accepted", missing)
			}
		})
	}
}

// TestParseLabelsRejectsUnsupportedAndInvalidValues 验证 schema 与恢复标识。
func TestParseLabelsRejectsUnsupportedAndInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "not managed", key: LabelManaged, value: "false"},
		{name: "unknown schema", key: LabelSchemaVersion, value: "3"},
		{name: "old runner protocol", key: LabelRunnerProtocolVersion, value: "0"},
		{name: "future runner protocol", key: LabelRunnerProtocolVersion, value: "2"},
		{name: "non-integer runner protocol", key: LabelRunnerProtocolVersion, value: "v1"},
		{name: "invalid expiry", key: LabelExpiresAt, value: "not-a-time"},
		{name: "invalid id", key: LabelSandboxID, value: "../sandbox"},
		{name: "invalid hash", key: LabelSpecHash, value: strings.Repeat("g", 64)},
		{name: "invalid workspace", key: LabelWorkspace, value: "/host/path"},
		{
			name:  "malicious long value",
			key:   LabelWorkspace,
			value: strings.Repeat("x", maxLabelValueLength+1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels := validTestLabels(t)
			labels[tt.key] = tt.value
			_, err := ParseLabels(labels)
			if err == nil {
				t.Fatal("expected parse rejection")
			}
			if strings.Contains(err.Error(), tt.value) {
				t.Fatalf("error leaked raw label value: %v", err)
			}
		})
	}
}

// TestLabelsV2CreationExpiryAndV1Compatibility 验证 v2 保存规范 UTC 创建快照且 v1 仍拒绝非空 expiry。
func TestLabelsV2CreationExpiryAndV1Compatibility(t *testing.T) {
	expiresAt := time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC)
	metadata := ManagedLabels{SandboxID: testSandboxID, SpecHash: testSpecHash, Workspace: testWorkspace, ExpiresAt: &expiresAt}
	labels, err := EncodeLabels(metadata)
	if err != nil || labels[LabelExpiresAt] != expiresAt.Format(time.RFC3339Nano) {
		t.Fatalf("encode expiry: %#v/%v", labels, err)
	}
	parsed, err := ParseLabels(labels)
	if err != nil || parsed.ExpiresAt == nil || !parsed.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("parse expiry: %#v/%v", parsed, err)
	}
	labels[LabelSchemaVersion] = labelSchemaVersionV1
	if _, err := ParseLabels(labels); err == nil {
		t.Fatal("schema v1 accepted creation expiry")
	}
}

// TestEncodeLabelsContainsNoSecretSurface 验证 codec 只能生成恢复白名单字段。
func TestEncodeLabelsContainsNoSecretSurface(t *testing.T) {
	labels := validTestLabels(t)
	for key, value := range labels {
		combined := strings.ToLower(key + "=" + value)
		for _, forbidden := range []string{
			"token",
			"password",
			"credential",
			"command",
			"environment",
		} {
			if strings.Contains(combined, forbidden) {
				t.Fatalf("label contains forbidden secret surface %q: %s", forbidden, key)
			}
		}
	}
}

// validTestLabels 返回通过 codec 生成的独立合法 map。
func validTestLabels(t *testing.T) map[string]string {
	t.Helper()
	labels, err := EncodeLabels(ManagedLabels{
		SandboxID:             testSandboxID,
		SpecHash:              testSpecHash,
		Workspace:             testWorkspace,
		RunnerProtocolVersion: runnerbootstrap.CurrentProtocolVersion,
	})
	if err != nil {
		t.Fatalf("encode valid labels: %v", err)
	}
	return labels
}

// cloneLabels 复制 label map，避免 table case 互相污染。
func cloneLabels(source map[string]string) map[string]string {
	copy := make(map[string]string, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}
