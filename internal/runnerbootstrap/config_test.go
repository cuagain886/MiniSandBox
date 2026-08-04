package runnerbootstrap

import (
	"bytes"
	"reflect"
	"testing"

	"minisandbox/internal/config"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	got, err := FromConfig(config.Default(), "018f1111-2222-7333-8444-555555555555", 0, 0)
	if err != nil {
		t.Fatalf("build bootstrap config: %v", err)
	}
	return got
}

// TestRoundTrip 验证可信配置经过 JSON 序列化后逐字段保持不变。
func TestRoundTrip(t *testing.T) {
	want := testConfig(t)
	encoded, err := Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := Unmarshal(encoded)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\ngot  %+v\nwant %+v", got, want)
	}
}

// TestUnmarshalRejectsUnknownAndMissingFields 验证内部协议保持封闭字段集合，
// 且顶层、身份、限制和路径任一必填字段缺失都会失败。
func TestUnmarshalRejectsUnknownAndMissingFields(t *testing.T) {
	encoded, err := Marshal(testConfig(t))
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	tests := []struct {
		name string
		data []byte
	}{
		{"unknown field", bytes.Replace(encoded, []byte(`"sandbox_id":`), []byte(`"unknown":true,"sandbox_id":`), 1)},
		{"missing top level", bytes.Replace(encoded, []byte(`"sandbox_id":"018f1111-2222-7333-8444-555555555555",`), nil, 1)},
		{"missing identity", bytes.Replace(encoded, []byte(`"execution_uid":1000,`), nil, 1)},
		{"missing limit", bytes.Replace(encoded, []byte(`"max_output_bytes":10485760,`), nil, 1)},
		{"missing path", bytes.Replace(encoded, []byte(`"socket_path":"/run/minisandbox/runner.sock",`), nil, 1)},
		{"trailing json", append(append([]byte(nil), encoded...), []byte(` {}`)...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Unmarshal(test.data); err == nil {
				t.Fatal("expected strict decode error")
			}
		})
	}
}

// TestFromConfigDerivesTrustedFields 验证所有字段仅来自控制面配置、固定路径、
// sandbox ID 和显式传入的 socket owner 身份。
func TestFromConfigDerivesTrustedFields(t *testing.T) {
	control := config.Default()
	control.Runner.MaxOutputBytes = 2_097_152
	got, err := FromConfig(control, "sandbox-1", 2000, 2001)
	if err != nil {
		t.Fatalf("from config: %v", err)
	}
	if got.ProtocolVersion != CurrentProtocolVersion || got.SandboxID != "sandbox-1" || got.Identity.SocketOwnerUID != 2000 || got.Identity.SocketOwnerGID != 2001 || got.Limits.MaxOutputBytes != 2_097_152 || got.Paths.SocketPath != SocketPath {
		t.Fatalf("bootstrap fields were not derived as expected: %+v", got)
	}
}

// TestFromConfigRejectsIdentityCollision 验证控制面与 execution 数字身份重合时
// 不会生成可供 runner 使用的配置。
func TestFromConfigRejectsIdentityCollision(t *testing.T) {
	if _, err := FromConfig(config.Default(), "sandbox-1", 1000, 2001); err == nil {
		t.Fatal("expected socket owner identity collision")
	}
}
