// Package artifact 以不依赖 Docker daemon 的静态测试锁定发布 artifact 安全契约。
package artifact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type artifactContract struct {
	SchemaVersion         int      `json:"schema_version"`
	Platform              string   `json:"platform"`
	GoBuilder             string   `json:"go_builder"`
	RuntimeBase           string   `json:"runtime_base"`
	NFTablesVersion       string   `json:"nftables_version"`
	EgressProtocolVersion int      `json:"egress_protocol_version"`
	RuleSchemaVersion     int      `json:"rule_schema_version"`
	Entrypoint            []string `json:"entrypoint"`
	User                  string   `json:"user"`
	ControlTransport      string   `json:"control_transport"`
	AttestationStorage    string   `json:"attestation_storage"`
	StdinOnce             bool     `json:"stdin_once"`
	LogDriver             string   `json:"log_driver"`
	ReleaseOutputs        []string `json:"release_outputs"`
}

// TestEgressArtifactContract 验证构建输入精确固定平台、base digest、nft 版本、入口、
// 非 root 用户和 release evidence 字段。
func TestEgressArtifactContract(t *testing.T) {
	root := repositoryRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "build", "egress", "artifact-contract.json"))
	if err != nil {
		t.Fatalf("read artifact contract: %v", err)
	}
	var contract artifactContract
	if err := json.Unmarshal(content, &contract); err != nil {
		t.Fatalf("decode artifact contract: %v", err)
	}
	if contract.SchemaVersion != 1 || contract.Platform != "linux/amd64" || contract.EgressProtocolVersion != 1 || contract.RuleSchemaVersion != 1 {
		t.Fatalf("artifact versions drifted: %+v", contract)
	}
	for field, value := range map[string]string{"go_builder": contract.GoBuilder, "runtime_base": contract.RuntimeBase} {
		if !strings.Contains(value, "@sha256:") || len(value[strings.LastIndex(value, "@sha256:")+8:]) != 64 {
			t.Fatalf("%s is not digest pinned: %q", field, value)
		}
	}
	if contract.NFTablesVersion != "1.0.6-2+deb12u2" || contract.User != "65532:65532" ||
		strings.Join(contract.Entrypoint, " ") != "/usr/local/bin/egressd bootstrap" ||
		contract.ControlTransport != "docker-attach-stdio" || contract.AttestationStorage != "process-memory" ||
		contract.StdinOnce || contract.LogDriver != "none" || len(contract.ReleaseOutputs) != 5 {
		t.Fatalf("artifact contract drifted: %+v", contract)
	}
}

// TestEgressDockerfileIsMinimal 验证 final stage 不继承构建 rootfs，且不存在 shell、
// 包管理器、runner、Docker CLI、healthcheck、端口或凭据注入面。
func TestEgressDockerfileIsMinimal(t *testing.T) {
	root := repositoryRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "build", "egress", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfile := string(content)
	required := []string{
		"FROM scratch", "COPY --from=nft-runtime /rootfs /", "COPY --from=go-build /out/egressd",
		"USER 65532:65532", `ENTRYPOINT ["/usr/local/bin/egressd", "bootstrap"]`,
		`nftables=${NFTABLES_VERSION}`, "CGO_ENABLED=0 GOOS=linux GOARCH=amd64",
		`awk '$(NF-2) == "=>" { print $(NF-1) }`, "cp -L --parents",
	}
	for _, marker := range required {
		if !strings.Contains(dockerfile, marker) {
			t.Fatalf("Dockerfile does not contain %q", marker)
		}
	}
	final := dockerfile[strings.LastIndex(dockerfile, "FROM scratch"):]
	for _, forbidden := range []string{"apt-get", "bash", "sh\"", "runnerd", "sandbox-init", "docker", "HEALTHCHECK", "EXPOSE", "VOLUME", "SECRET", "PASSWORD", "TOKEN"} {
		if strings.Contains(final, forbidden) {
			t.Fatalf("final image stage contains forbidden component %q", forbidden)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}
