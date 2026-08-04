package contract_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"minisandbox/internal/runnerbootstrap"
	"minisandbox/pkg/protocol"
)

// TestRunnerHealthWireContract 钉死 health 的字段名、整数协议版本和 Linux
// netns identity 格式，避免 runner 与控制面各自演进造成静默兼容。
func TestRunnerHealthWireContract(t *testing.T) {
	fixture := []byte(`{"status":"ok","service":"runnerd","version":"test-build","protocol_version":1,"netns_identity":"linux-netns:4:4026532000"}`)
	var got protocol.RunnerHealth
	if err := json.Unmarshal(fixture, &got); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	want := protocol.RunnerHealth{
		Status:          "ok",
		Service:         "runnerd",
		Version:         "test-build",
		ProtocolVersion: runnerbootstrap.CurrentProtocolVersion,
		NetNSIdentity:   "linux-netns:4:4026532000",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("health fixture drift:\ngot  %+v\nwant %+v", got, want)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("encode health: %v", err)
	}
	if string(encoded) != string(fixture) {
		t.Fatalf("health wire drift:\ngot  %s\nwant %s", encoded, fixture)
	}
	if err := protocol.ValidateRunnerNetNSIdentity(got.NetNSIdentity); err != nil {
		t.Fatalf("fixture identity invalid: %v", err)
	}
}

// TestRunnerHealthOpenAPIContract 验证 runner OpenAPI 公开同一组必填 health
// 字段，并禁止未声明字段。
func TestRunnerHealthOpenAPIContract(t *testing.T) {
	runner := readRunnerOpenAPI(t)
	for _, fragment := range []string{
		"$ref: \"#/components/schemas/RunnerHealth\"",
		"required: [status, service, version, protocol_version, netns_identity]",
		"additionalProperties: false",
		"pattern: '^linux-netns:[1-9][0-9]*:[1-9][0-9]*$'",
	} {
		if !strings.Contains(runner, fragment) {
			t.Errorf("runner health OpenAPI is missing %q", fragment)
		}
	}
}
