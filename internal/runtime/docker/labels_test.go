package docker

import (
	"reflect"
	"strings"
	"testing"
)

const (
	testSandboxID = "00010203-0405-4607-8809-0a0b0c0d0e0f"
	testSpecHash  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testWorkspace = "minisandbox-workspace-" + testSandboxID
)

// TestLabelsRoundTrip 验证固定六键契约可以无损编解码。
func TestLabelsRoundTrip(t *testing.T) {
	metadata := ManagedLabels{
		SandboxID: testSandboxID,
		SpecHash:  testSpecHash,
		Workspace: testWorkspace,
	}
	labels, err := EncodeLabels(metadata)
	if err != nil {
		t.Fatalf("encode labels: %v", err)
	}
	if len(labels) != len(managedLabelKeys) {
		t.Fatalf("label count: got %d, want %d", len(labels), len(managedLabelKeys))
	}
	if labels[LabelManaged] != "true" ||
		labels[LabelSchemaVersion] != "1" ||
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
		{name: "unknown schema", key: LabelSchemaVersion, value: "2"},
		{name: "phase one expiry", key: LabelExpiresAt, value: "2030-01-01T00:00:00Z"},
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
		SandboxID: testSandboxID,
		SpecHash:  testSpecHash,
		Workspace: testWorkspace,
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
