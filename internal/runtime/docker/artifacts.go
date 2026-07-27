package docker

import (
	"bytes"
	"debug/elf"
	"errors"
	"fmt"

	embeddedassets "minisandbox/internal/embedded"
)

const (
	// RunnerArtifactName 是注入容器的 runner 数据面二进制文件名。
	RunnerArtifactName = "runnerd"
	// InitArtifactName 是作为容器 PID 1 的 init 二进制文件名。
	InitArtifactName = "sandbox-init"
)

// Artifact 保存一个已经验证的平台二进制。
type Artifact struct {
	// Name 是容器内构建流程约定的固定文件名。
	Name string
	// Data 是 linux/amd64 ELF 可执行文件内容。
	Data []byte
}

// ArtifactSet 保存 Phase 1 注入 sandbox 所需的完整二进制集合。
type ArtifactSet struct {
	// Runner 是容器内执行数据面 runnerd。
	Runner Artifact
	// Init 是容器 PID 1 sandbox-init。
	Init Artifact
}

// ArtifactProvider 提供启动时已完成平台校验的嵌入式二进制。
type ArtifactProvider interface {
	// Artifacts 返回独立数据副本，调用方修改后不能污染后续 sandbox 创建。
	Artifacts() ArtifactSet
}

// EmbeddedArtifactProvider 包装 internal/embedded 中的生产构建产物。
//
// 构造成功表示 runnerd 和 sandbox-init 均已通过基础 ELF 校验；无效产物
// 必须阻止启动进入 ready，而不能延迟到容器创建中途才失败。
type EmbeddedArtifactProvider struct {
	artifacts ArtifactSet
}

// NewEmbeddedArtifactProvider 读取并校验构建流程生成的 linux/amd64 产物。
func NewEmbeddedArtifactProvider() (*EmbeddedArtifactProvider, error) {
	return newArtifactProvider(embeddedReader{})
}

// Artifacts 返回文件名和值均独立的产物副本。
func (p *EmbeddedArtifactProvider) Artifacts() ArtifactSet {
	if p == nil {
		return ArtifactSet{}
	}
	return cloneArtifactSet(p.artifacts)
}

// artifactReader 是 provider 从嵌入源读取指定文件的最小能力。
type artifactReader interface {
	ReadLinuxAMD64(name string) ([]byte, error)
}

// embeddedReader 把 internal/embedded 包函数适配为可替换 reader。
type embeddedReader struct{}

// ReadLinuxAMD64 从生产 embed.FS 读取固定平台构建产物。
func (embeddedReader) ReadLinuxAMD64(name string) ([]byte, error) {
	return embeddedassets.ReadLinuxAMD64(name)
}

// newArtifactProvider 使用可注入 reader 读取两个固定文件并立即校验。
func newArtifactProvider(reader artifactReader) (*EmbeddedArtifactProvider, error) {
	if reader == nil {
		return nil, errors.New("artifact reader must not be nil")
	}
	runner, err := reader.ReadLinuxAMD64(RunnerArtifactName)
	if err != nil {
		return nil, fmt.Errorf("read runner artifact: %w", err)
	}
	init, err := reader.ReadLinuxAMD64(InitArtifactName)
	if err != nil {
		return nil, fmt.Errorf("read init artifact: %w", err)
	}
	artifacts := ArtifactSet{
		Runner: Artifact{Name: RunnerArtifactName, Data: runner},
		Init:   Artifact{Name: InitArtifactName, Data: init},
	}
	if err := validateArtifactSet(artifacts); err != nil {
		return nil, err
	}
	return &EmbeddedArtifactProvider{
		artifacts: cloneArtifactSet(artifacts),
	}, nil
}

// validateArtifactSet 验证固定名称以及两个 linux/amd64 ELF64 可执行文件。
func validateArtifactSet(artifacts ArtifactSet) error {
	if artifacts.Runner.Name != RunnerArtifactName {
		return errors.New("runner artifact has an unexpected name")
	}
	if artifacts.Init.Name != InitArtifactName {
		return errors.New("init artifact has an unexpected name")
	}
	if err := validateLinuxAMD64ELF(artifacts.Runner.Data); err != nil {
		return fmt.Errorf("validate runner artifact: %w", err)
	}
	if err := validateLinuxAMD64ELF(artifacts.Init.Data); err != nil {
		return fmt.Errorf("validate init artifact: %w", err)
	}
	return nil
}

// validateLinuxAMD64ELF 使用标准库 parser 验证基础平台和可执行类型。
func validateLinuxAMD64ELF(data []byte) error {
	if len(data) == 0 {
		return errors.New("artifact is empty")
	}
	file, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return errors.New("artifact is not an ELF file")
	}
	defer file.Close()

	if file.Class != elf.ELFCLASS64 {
		return errors.New("artifact is not ELF64")
	}
	if file.Data != elf.ELFDATA2LSB {
		return errors.New("artifact is not little-endian")
	}
	if file.Machine != elf.EM_X86_64 {
		return errors.New("artifact is not amd64")
	}
	if file.Type != elf.ET_EXEC {
		return errors.New("artifact is not an executable")
	}
	return nil
}

// cloneArtifactSet 深拷贝两个产物，避免可变 []byte 跨调用共享。
func cloneArtifactSet(source ArtifactSet) ArtifactSet {
	return ArtifactSet{
		Runner: Artifact{
			Name: source.Runner.Name,
			Data: append([]byte(nil), source.Runner.Data...),
		},
		Init: Artifact{
			Name: source.Init.Name,
			Data: append([]byte(nil), source.Init.Data...),
		},
	}
}

var _ ArtifactProvider = (*EmbeddedArtifactProvider)(nil)
