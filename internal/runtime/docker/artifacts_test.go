package docker

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"reflect"
	"testing"
)

// fakeArtifactReader 从内存 map 返回产物并记录读取顺序。
type fakeArtifactReader struct {
	files map[string][]byte
	errs  map[string]error
	calls []string
}

// ReadLinuxAMD64 返回指定测试文件的独立副本。
func (f *fakeArtifactReader) ReadLinuxAMD64(name string) ([]byte, error) {
	f.calls = append(f.calls, name)
	if err := f.errs[name]; err != nil {
		return nil, err
	}
	data, ok := f.files[name]
	if !ok {
		return nil, errors.New("artifact missing")
	}
	return append([]byte(nil), data...), nil
}

// TestNewArtifactProviderSuccess 验证两个固定文件、ELF 校验与副本隔离。
func TestNewArtifactProviderSuccess(t *testing.T) {
	executable := testELF64AMD64()
	reader := &fakeArtifactReader{
		files: map[string][]byte{
			RunnerArtifactName: executable,
			InitArtifactName:   executable,
		},
	}

	provider, err := newArtifactProvider(reader)
	if err != nil {
		t.Fatalf("new artifact provider: %v", err)
	}
	if want := []string{RunnerArtifactName, InitArtifactName}; !reflect.DeepEqual(reader.calls, want) {
		t.Fatalf("read calls: got %v, want %v", reader.calls, want)
	}

	first := provider.Artifacts()
	if first.Runner.Name != RunnerArtifactName ||
		first.Init.Name != InitArtifactName ||
		len(first.Runner.Data) == 0 ||
		len(first.Init.Data) == 0 {
		t.Fatalf("artifacts: %#v", first)
	}
	first.Runner.Data[0] = 0
	second := provider.Artifacts()
	if second.Runner.Data[0] != 0x7f {
		t.Fatal("caller mutation polluted stored artifact")
	}
}

// TestEmbeddedArtifactProviderGeneratedAssets 在构建产物存在时验证真实 embed 路径。
//
// 普通源码测试不会提交生成二进制，因此缺失时跳过；Makefile/Dockerfile 先
// 生成产物再编译 sandboxd，此时本测试必须完成真实读取和 ELF 校验。
func TestEmbeddedArtifactProviderGeneratedAssets(t *testing.T) {
	provider, err := NewEmbeddedArtifactProvider()
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("generated linux/amd64 artifacts are not present")
	}
	if err != nil {
		t.Fatalf("validate generated embedded artifacts: %v", err)
	}
	artifacts := provider.Artifacts()
	if len(artifacts.Runner.Data) == 0 || len(artifacts.Init.Data) == 0 {
		t.Fatal("generated provider returned empty artifacts")
	}
}

// TestNewArtifactProviderRejectsMissingArtifact 验证任一文件缺失会提前失败。
func TestNewArtifactProviderRejectsMissingArtifact(t *testing.T) {
	reader := &fakeArtifactReader{
		files: map[string][]byte{
			RunnerArtifactName: testELF64AMD64(),
		},
	}

	if provider, err := newArtifactProvider(reader); err == nil || provider != nil {
		t.Fatalf("missing artifact result: provider=%#v err=%v", provider, err)
	}
}

// TestNewArtifactProviderRejectsInvalidArtifacts 验证空文件和错误 magic。
func TestNewArtifactProviderRejectsInvalidArtifacts(t *testing.T) {
	tests := []struct {
		name   string
		runner []byte
	}{
		{name: "empty", runner: nil},
		{name: "wrong magic", runner: []byte("not-an-elf")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &fakeArtifactReader{
				files: map[string][]byte{
					RunnerArtifactName: tt.runner,
					InitArtifactName:   testELF64AMD64(),
				},
			}
			if provider, err := newArtifactProvider(reader); err == nil ||
				provider != nil {
				t.Fatalf("invalid artifact result: provider=%#v err=%v", provider, err)
			}
		})
	}
}

// TestValidateArtifactSetRejectsNameAndPlatform 验证名称、架构和文件类型。
func TestValidateArtifactSetRejectsNameAndPlatform(t *testing.T) {
	valid := testELF64AMD64()
	tests := []struct {
		name   string
		mutate func(*ArtifactSet)
	}{
		{
			name: "runner name",
			mutate: func(set *ArtifactSet) {
				set.Runner.Name = "../runnerd"
			},
		},
		{
			name: "init name",
			mutate: func(set *ArtifactSet) {
				set.Init.Name = "init"
			},
		},
		{
			name: "wrong architecture",
			mutate: func(set *ArtifactSet) {
				set.Runner.Data = testELF(elfMachineAArch64, elfTypeExecutable)
			},
		},
		{
			name: "shared object",
			mutate: func(set *ArtifactSet) {
				set.Init.Data = testELF(elfMachineAMD64, elfTypeSharedObject)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := ArtifactSet{
				Runner: Artifact{Name: RunnerArtifactName, Data: valid},
				Init:   Artifact{Name: InitArtifactName, Data: valid},
			}
			tt.mutate(&set)
			if err := validateArtifactSet(set); err == nil {
				t.Fatal("expected artifact rejection")
			}
		})
	}
}

const (
	elfMachineAMD64       = 62
	elfMachineAArch64     = 183
	elfTypeExecutable     = 2
	elfTypeSharedObject   = 3
	minimumELFHeaderBytes = 64
)

// testELF64AMD64 返回 parser 可识别的最小 ELF64 little-endian amd64 header。
func testELF64AMD64() []byte {
	return testELF(elfMachineAMD64, elfTypeExecutable)
}

// testELF 构造不含 program/section header 的最小 ELF64 测试文件。
func testELF(machine uint16, fileType uint16) []byte {
	header := make([]byte, minimumELFHeaderBytes)
	copy(header[0:4], []byte{0x7f, 'E', 'L', 'F'})
	header[4] = 2 // ELFCLASS64
	header[5] = 1 // ELFDATA2LSB
	header[6] = 1 // EV_CURRENT
	binary.LittleEndian.PutUint16(header[16:18], fileType)
	binary.LittleEndian.PutUint16(header[18:20], machine)
	binary.LittleEndian.PutUint32(header[20:24], 1)
	binary.LittleEndian.PutUint16(header[52:54], minimumELFHeaderBytes)
	binary.LittleEndian.PutUint16(header[54:56], 56)
	binary.LittleEndian.PutUint16(header[58:60], 64)
	return header
}
