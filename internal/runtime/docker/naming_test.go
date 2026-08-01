package docker

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestNamesForSandbox 验证合法 ID 生成固定名称、受管路径且没有文件系统副作用。
func TestNamesForSandbox(t *testing.T) {
	dataDirectory := filepath.Join(t.TempDir(), "data")

	names, err := NamesForSandbox(dataDirectory, testSandboxID)
	if err != nil {
		t.Fatalf("generate names: %v", err)
	}
	if got, want := names.Container,
		"minisandbox-"+testSandboxID; got != want {
		t.Fatalf("container: got %q, want %q", got, want)
	}
	if got, want := names.Workspace,
		"minisandbox-workspace-"+testSandboxID; got != want {
		t.Fatalf("workspace: got %q, want %q", got, want)
	}
	runRoot := filepath.Join(dataDirectory, "run")
	if got, want := names.RuntimeDirectory,
		filepath.Join(runRoot, testSandboxID); got != want {
		t.Fatalf("runtime directory: got %q, want %q", got, want)
	}
	if got, want := names.HostRunnerSocket,
		filepath.Join(runRoot, testSandboxID, "runner.sock"); got != want {
		t.Fatalf("host socket: got %q, want %q", got, want)
	}
	relative, err := filepath.Rel(runRoot, names.RuntimeDirectory)
	if err != nil || relative != testSandboxID {
		t.Fatalf("runtime directory relative path: %q, err=%v", relative, err)
	}

	if _, err := os.Lstat(dataDirectory); !os.IsNotExist(err) {
		t.Fatalf("pure naming helper touched filesystem: %v", err)
	}
}

// TestNamesForSandboxRejectsUnsafeInput 验证路径穿越、分隔符和超长输入。
func TestNamesForSandboxRejectsUnsafeInput(t *testing.T) {
	dataDirectory := filepath.Join(t.TempDir(), "data")
	tests := []struct {
		name string
		root string
		id   string
	}{
		{name: "relative data directory", root: "data", id: testSandboxID},
		{name: "path traversal", root: dataDirectory, id: "../sandbox"},
		{name: "forward separator", root: dataDirectory, id: testSandboxID + "/x"},
		{name: "back separator", root: dataDirectory, id: testSandboxID + `\x`},
		{
			name: "overlong ID",
			root: dataDirectory,
			id:   strings.Repeat("a", maxDockerResourceNameBytes+1),
		},
		{
			name: "overlong data path",
			root: filepath.Join(
				filepath.VolumeName(dataDirectory)+string(filepath.Separator),
				strings.Repeat("x", maxManagedPathBytes),
			),
			id: testSandboxID,
		},
	}
	if runtime.GOOS == "linux" {
		tests = append(tests, struct {
			name string
			root string
			id   string
		}{
			name: "overlong runner socket path",
			root: filepath.Join(
				filepath.VolumeName(dataDirectory)+string(filepath.Separator),
				strings.Repeat("s", maxUnixSocketPathBytes),
			),
			id: testSandboxID,
		})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NamesForSandbox(tt.root, tt.id); err == nil {
				t.Fatal("expected unsafe input rejection")
			}
		})
	}
}

// TestLabelsRequireDeterministicWorkspace 验证恢复 label 不能指向其他 volume。
func TestLabelsRequireDeterministicWorkspace(t *testing.T) {
	labels := validTestLabels(t)
	labels[LabelWorkspace] = "minisandbox-workspace-other"

	if _, err := ParseLabels(labels); err == nil {
		t.Fatal("expected mismatched workspace rejection")
	}
}
