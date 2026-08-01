package docker

import (
	"archive/tar"
	"bytes"
	"io"
	"reflect"
	"testing"
)

// staticArtifactProvider 返回测试指定的固定产物集合。
type staticArtifactProvider struct {
	artifacts ArtifactSet
}

// Artifacts 返回预设产物。
func (p staticArtifactProvider) Artifacts() ArtifactSet {
	return cloneArtifactSet(p.artifacts)
}

// TestBuildArtifactTar 验证固定目录、文件路径、权限、属主、mtime 和 entry 数量。
func TestBuildArtifactTar(t *testing.T) {
	runnerData := testELF64AMD64()
	initData := append(testELF64AMD64(), []byte("init")...)
	provider := staticArtifactProvider{
		artifacts: ArtifactSet{
			Runner: Artifact{Name: RunnerArtifactName, Data: runnerData},
			Init:   Artifact{Name: InitArtifactName, Data: initData},
		},
	}

	archive, err := BuildArtifactTar(provider)
	if err != nil {
		t.Fatalf("build artifact tar: %v", err)
	}
	defer archive.Close()

	reader := tar.NewReader(archive)
	for _, want := range []string{"opt", "opt/minisandbox"} {
		header, err := reader.Next()
		if err != nil {
			t.Fatalf("read %s directory header: %v", want, err)
		}
		if header.Name != want ||
			header.Mode != artifactDirectoryMode ||
			header.Uid != 0 ||
			header.Gid != 0 ||
			header.Typeflag != tar.TypeDir ||
			!header.ModTime.Equal(artifactModTime) {
			t.Fatalf("directory header: %#v", header)
		}
	}
	expected := []struct {
		name string
		data []byte
	}{
		{name: "opt/minisandbox/" + RunnerArtifactName, data: runnerData},
		{name: "opt/minisandbox/" + InitArtifactName, data: initData},
	}
	for _, want := range expected {
		header, err := reader.Next()
		if err != nil {
			t.Fatalf("read %s header: %v", want.name, err)
		}
		if header.Name != want.name {
			t.Fatalf("entry name: got %q, want %q", header.Name, want.name)
		}
		if header.Mode != artifactFileMode {
			t.Fatalf("%s mode: got %o, want 755", want.name, header.Mode)
		}
		if header.Uid != 0 || header.Gid != 0 {
			t.Fatalf("%s owner: uid=%d gid=%d", want.name, header.Uid, header.Gid)
		}
		if !header.ModTime.Equal(artifactModTime) {
			t.Fatalf(
				"%s mtime: got %s, want %s",
				want.name,
				header.ModTime,
				artifactModTime,
			)
		}
		if header.Typeflag != tar.TypeReg {
			t.Fatalf("%s type: got %d, want regular", want.name, header.Typeflag)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read %s content: %v", want.name, err)
		}
		if !bytes.Equal(content, want.data) {
			t.Fatalf("%s content mismatch", want.name)
		}
	}
	if header, err := reader.Next(); err != io.EOF || header != nil {
		t.Fatalf("unexpected extra tar entry: header=%#v err=%v", header, err)
	}
}

// TestBuildArtifactTarDeterministic 验证固定元数据使重复编码字节一致。
func TestBuildArtifactTarDeterministic(t *testing.T) {
	provider := staticArtifactProvider{
		artifacts: ArtifactSet{
			Runner: Artifact{Name: RunnerArtifactName, Data: testELF64AMD64()},
			Init:   Artifact{Name: InitArtifactName, Data: testELF64AMD64()},
		},
	}

	first := readArtifactArchive(t, provider)
	second := readArtifactArchive(t, provider)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("artifact tar output is not deterministic")
	}
}

// TestBuildArtifactTarRejectsUnexpectedProvider 验证调用方不能注入路径或空产物。
func TestBuildArtifactTarRejectsUnexpectedProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider ArtifactProvider
	}{
		{name: "nil provider", provider: nil},
		{
			name: "path injection",
			provider: staticArtifactProvider{
				artifacts: ArtifactSet{
					Runner: Artifact{
						Name: "../runnerd",
						Data: testELF64AMD64(),
					},
					Init: Artifact{
						Name: InitArtifactName,
						Data: testELF64AMD64(),
					},
				},
			},
		},
		{
			name: "empty init",
			provider: staticArtifactProvider{
				artifacts: ArtifactSet{
					Runner: Artifact{
						Name: RunnerArtifactName,
						Data: testELF64AMD64(),
					},
					Init: Artifact{Name: InitArtifactName},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archive, err := BuildArtifactTar(tt.provider)
			if err == nil || archive != nil {
				t.Fatalf("invalid provider result: archive=%#v err=%v", archive, err)
			}
		})
	}
}

// readArtifactArchive 构建并读取完整 tar 字节。
func readArtifactArchive(
	t *testing.T,
	provider ArtifactProvider,
) []byte {
	t.Helper()
	archive, err := BuildArtifactTar(provider)
	if err != nil {
		t.Fatalf("build artifact tar: %v", err)
	}
	defer archive.Close()
	content, err := io.ReadAll(archive)
	if err != nil {
		t.Fatalf("read artifact tar: %v", err)
	}
	return content
}
