package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// LeaseManifestName 是每个受管 runtime directory 内唯一允许的租约投影文件名。
	LeaseManifestName = "lease.json"
	// LeaseManifestSchemaVersion 是当前严格 JSON 格式版本。
	LeaseManifestSchemaVersion = 1
	// MaxLeaseManifestBytes 限制租约投影及恢复读取的内存占用。
	MaxLeaseManifestBytes = 1024
)

// LeaseManifest 是 Store 当前租约的最小、安全、非权威文件投影。
type LeaseManifest struct {
	// SchemaVersion 固定为 1；未知版本必须拒绝。
	SchemaVersion int `json:"schema_version"`
	// SandboxID 是 runtime directory 名称对应的规范 UUID v4。
	SandboxID string `json:"sandbox_id"`
	// SpecHash 是 Store resolved spec 的小写 SHA-256。
	SpecHash string `json:"spec_hash"`
	// ExpiresAt 是 Store 权威租约的 UTC 绝对时间。
	ExpiresAt time.Time `json:"expires_at"`
	// ProjectedStoreRevision 是生成此投影时读取的 Store revision。
	ProjectedStoreRevision uint64 `json:"projected_store_revision"`
}

// EncodeLeaseManifest 校验字段 allowlist 并生成不超过 1 KiB 的稳定 JSON。
func EncodeLeaseManifest(manifest LeaseManifest) ([]byte, error) {
	manifest.ExpiresAt = manifest.ExpiresAt.UTC()
	if err := validateLeaseManifest(manifest); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode lease manifest: %w", err)
	}
	encoded = append(encoded, '\n')
	if len(encoded) > MaxLeaseManifestBytes {
		return nil, errors.New("lease manifest exceeds size limit")
	}
	return encoded, nil
}

// DecodeLeaseManifest 严格解析单个、小尺寸、无未知字段的租约投影。
func DecodeLeaseManifest(content []byte) (LeaseManifest, error) {
	if len(content) == 0 || len(content) > MaxLeaseManifestBytes {
		return LeaseManifest{}, errors.New("lease manifest size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var manifest LeaseManifest
	if err := decoder.Decode(&manifest); err != nil {
		return LeaseManifest{}, errors.New("lease manifest JSON is invalid")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return LeaseManifest{}, errors.New("lease manifest contains trailing data")
	}
	if manifest.ExpiresAt.Location() != time.UTC {
		return LeaseManifest{}, errors.New("lease manifest expiry must be UTC")
	}
	if err := validateLeaseManifest(manifest); err != nil {
		return LeaseManifest{}, err
	}
	return manifest, nil
}

func validateLeaseManifest(manifest LeaseManifest) error {
	if manifest.SchemaVersion != LeaseManifestSchemaVersion || !validLeaseSandboxID(manifest.SandboxID) ||
		!validLeaseHex(manifest.SpecHash, 64) || manifest.ExpiresAt.IsZero() ||
		manifest.ExpiresAt.Year() < 0 || manifest.ExpiresAt.Year() > 9999 || manifest.ProjectedStoreRevision == 0 {
		return errors.New("lease manifest fields are invalid")
	}
	return nil
}

// LeaseManifestWriter 只根据受信 run root 和 sandbox ID 选择固定投影路径。
type LeaseManifestWriter struct {
	runRoot string
	rename  func(string, string) error
	syncDir func(string) error
}

// NewLeaseManifestWriter 创建不接受任意目标路径的原子 manifest writer。
func NewLeaseManifestWriter(runRoot string) (*LeaseManifestWriter, error) {
	if !filepath.IsAbs(runRoot) {
		return nil, errors.New("lease manifest run root must be absolute")
	}
	return &LeaseManifestWriter{runRoot: filepath.Clean(runRoot), rename: os.Rename, syncDir: syncLeaseDirectory}, nil
}

// Write 在既有受管 runtime directory 内执行 temp、fsync、rename、parent fsync。
func (w *LeaseManifestWriter) Write(manifest LeaseManifest) (err error) {
	if w == nil || manifest.SandboxID == "" {
		return errors.New("lease manifest writer is invalid")
	}
	content, err := EncodeLeaseManifest(manifest)
	if err != nil {
		return err
	}
	directory := filepath.Join(w.runRoot, manifest.SandboxID)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("lease manifest runtime directory is unsafe")
	}
	target := filepath.Join(directory, LeaseManifestName)
	if info, err := os.Lstat(target); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return errors.New("lease manifest target is unsafe")
	} else if err != nil && !os.IsNotExist(err) {
		return errors.New("inspect lease manifest target")
	}
	temporary, err := os.CreateTemp(directory, ".lease-*.tmp")
	if err != nil {
		return fmt.Errorf("create lease manifest temp: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("restrict lease manifest temp: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write lease manifest temp: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync lease manifest temp: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close lease manifest temp: %w", err)
	}
	if err := w.rename(temporaryName, target); err != nil {
		return fmt.Errorf("replace lease manifest: %w", err)
	}
	if err := w.syncDir(directory); err != nil {
		return fmt.Errorf("sync lease manifest directory: %w", err)
	}
	return nil
}

func syncLeaseDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	err = handle.Sync()
	if runtime.GOOS == "windows" {
		return nil
	}
	return err
}

func validLeaseSandboxID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || id[14] != '4' || !strings.ContainsRune("89ab", rune(id[19])) {
		return false
	}
	for index := range id {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", rune(id[index])) {
			return false
		}
	}
	return true
}

func validLeaseHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}
