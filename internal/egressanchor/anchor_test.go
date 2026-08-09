package egressanchor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/egressnft"
	"minisandbox/internal/egresspolicy"
)

// TestActivatePublishesReadyAfterDrop 验证 bootstrap stdin、netns、降权回验和
// attestation 发布按顺序完成，取消信号使纯 anchor 正常退出。
func TestActivatePublishesReadyAfterDrop(t *testing.T) {
	bootstrap := testBootstrap(t)
	path := filepath.Join(t.TempDir(), "attestation.json")
	platform := &fakePlatform{
		networkNamespace: bootstrap.NetworkNamespace,
		snapshot:         Snapshot{UID: bootstrap.AnchorUID, GID: bootstrap.AnchorGID},
	}
	input := &fakeCloser{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Activate(ctx, bootstrap, Options{
		Platform: platform, BootstrapInput: input, AttestationPath: path,
		Now: func() time.Time { return time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC) },
	}); err != nil {
		t.Fatalf("activate anchor: %v", err)
	}
	if !input.closed || platform.dropUID != bootstrap.AnchorUID || platform.dropGID != bootstrap.AnchorGID {
		t.Fatalf("bootstrap resources were not closed/dropped: input=%+v platform=%+v", input, platform)
	}
	attestation, err := ReadAttestation(path)
	if err != nil {
		t.Fatalf("read attestation: %v", err)
	}
	if attestation.PolicyHash != bootstrap.Policy.Hash || attestation.NetworkNamespace != bootstrap.NetworkNamespace || attestation.ImageDigest != bootstrap.ImageDigest {
		t.Fatalf("attestation mismatch: %+v", attestation)
	}
}

// TestActivateFailClosed 验证 netns 漂移、降权失败、身份/capability 回验失败和 stdin
// 关闭失败均不会创建 readiness 文件。
func TestActivateFailClosed(t *testing.T) {
	bootstrap := testBootstrap(t)
	const netAdmin = uint64(1) << 12
	tests := []struct {
		name     string
		platform *fakePlatform
		closer   *fakeCloser
	}{
		{name: "stdin close failure", platform: validPlatform(bootstrap), closer: &fakeCloser{err: errors.New("close")}},
		{name: "netns lookup failure", platform: &fakePlatform{networkErr: errors.New("stat")}, closer: &fakeCloser{}},
		{name: "netns mismatch", platform: &fakePlatform{networkNamespace: "linux-netns:1:2"}, closer: &fakeCloser{}},
		{name: "drop failure", platform: &fakePlatform{networkNamespace: bootstrap.NetworkNamespace, dropErr: errors.New("capset")}, closer: &fakeCloser{}},
		{name: "snapshot failure", platform: &fakePlatform{networkNamespace: bootstrap.NetworkNamespace, snapshotErr: errors.New("status")}, closer: &fakeCloser{}},
		{name: "wrong uid", platform: &fakePlatform{networkNamespace: bootstrap.NetworkNamespace, snapshot: Snapshot{UID: 1, GID: bootstrap.AnchorGID}}, closer: &fakeCloser{}},
		{name: "additional supplementary group", platform: &fakePlatform{networkNamespace: bootstrap.NetworkNamespace, snapshot: Snapshot{UID: bootstrap.AnchorUID, GID: bootstrap.AnchorGID, SupplementaryGroups: []uint32{bootstrap.AnchorGID, 10}}}, closer: &fakeCloser{}},
		{name: "effective NET_ADMIN", platform: &fakePlatform{networkNamespace: bootstrap.NetworkNamespace, snapshot: Snapshot{UID: bootstrap.AnchorUID, GID: bootstrap.AnchorGID, CapEffective: netAdmin}}, closer: &fakeCloser{}},
		{name: "permitted NET_ADMIN", platform: &fakePlatform{networkNamespace: bootstrap.NetworkNamespace, snapshot: Snapshot{UID: bootstrap.AnchorUID, GID: bootstrap.AnchorGID, CapPermitted: netAdmin}}, closer: &fakeCloser{}},
		{name: "ambient NET_ADMIN", platform: &fakePlatform{networkNamespace: bootstrap.NetworkNamespace, snapshot: Snapshot{UID: bootstrap.AnchorUID, GID: bootstrap.AnchorGID, CapAmbient: netAdmin}}, closer: &fakeCloser{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "attestation.json")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := Activate(ctx, bootstrap, Options{Platform: test.platform, BootstrapInput: test.closer, AttestationPath: path}); err == nil {
				t.Fatal("expected activation failure")
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("failed anchor declared Ready: %v", err)
			}
		})
	}
}

// TestVerifySnapshotAllowsOnlyPrimarySupplementaryGroup 验证 Docker/runc 重复主 GID
// 不被误判为额外权限，同时任何不同 GID 都继续 fail closed。
func TestVerifySnapshotAllowsOnlyPrimarySupplementaryGroup(t *testing.T) {
	const uid, gid = uint32(65532), uint32(65532)
	for _, groups := range [][]uint32{nil, {gid}, {gid, gid}} {
		if err := verifySnapshot(Snapshot{UID: uid, GID: gid, SupplementaryGroups: groups}, uid, gid); err != nil {
			t.Fatalf("equivalent primary groups rejected: groups=%v err=%v", groups, err)
		}
	}
	if err := verifySnapshot(Snapshot{UID: uid, GID: gid, SupplementaryGroups: []uint32{gid, 1000}}, uid, gid); err == nil {
		t.Fatal("additional group privilege was accepted")
	}
}

// TestAttestationValidation 验证文件类型、权限、大小、封闭 schema 与不可覆盖语义。
func TestAttestationValidation(t *testing.T) {
	bootstrap := testBootstrap(t)
	valid := Attestation{
		ProtocolVersion: bootstrap.Policy.ProtocolVersion, RuleSchemaVersion: bootstrap.Policy.RuleSchemaVersion,
		PolicyHash: bootstrap.Policy.Hash, NetworkNamespace: bootstrap.NetworkNamespace,
		ImageDigest: bootstrap.ImageDigest, CreatedAt: time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC),
	}
	path := filepath.Join(t.TempDir(), "attestation.json")
	if err := WriteAttestation(path, valid); err != nil {
		t.Fatalf("write attestation: %v", err)
	}
	if err := WriteAttestation(path, valid); err == nil {
		t.Fatal("existing attestation was overwritten")
	}
	if _, err := ReadAttestation(path); err != nil {
		t.Fatalf("read valid attestation: %v", err)
	}

	tests := []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{name: "unknown field", content: `{"unknown":true}`, mode: 0o400},
		{name: "trailing JSON", content: `{}` + `{}`, mode: 0o400},
		{name: "oversized", content: strings.Repeat("x", MaxAttestationBytes+1), mode: 0o400},
		{name: "writable", content: `{}`, mode: 0o600},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := filepath.Join(t.TempDir(), "candidate")
			if err := os.WriteFile(candidate, []byte(test.content), test.mode); err != nil {
				t.Fatalf("write candidate: %v", err)
			}
			if _, err := ReadAttestation(candidate); err == nil {
				t.Fatal("invalid attestation accepted")
			}
		})
	}
}

func testBootstrap(t *testing.T) egressnft.Bootstrap {
	t.Helper()
	policy, err := egresspolicy.Build(nil, nil)
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	return egressnft.Bootstrap{
		Policy: policy, NetworkNamespace: "linux-netns:4:4026533000",
		ImageDigest: "registry.example/egressd@sha256:" + strings.Repeat("a", 64),
		AnchorUID:   65532, AnchorGID: 65532,
	}
}

func validPlatform(bootstrap egressnft.Bootstrap) *fakePlatform {
	return &fakePlatform{networkNamespace: bootstrap.NetworkNamespace, snapshot: Snapshot{
		UID: bootstrap.AnchorUID, GID: bootstrap.AnchorGID, SupplementaryGroups: []uint32{bootstrap.AnchorGID},
	}}
}

type fakePlatform struct {
	networkNamespace string
	networkErr       error
	dropErr          error
	snapshot         Snapshot
	snapshotErr      error
	dropUID          uint32
	dropGID          uint32
}

func (platform *fakePlatform) NetworkNamespace() (string, error) {
	return platform.networkNamespace, platform.networkErr
}

func (platform *fakePlatform) DropPrivileges(uid, gid uint32) error {
	platform.dropUID, platform.dropGID = uid, gid
	return platform.dropErr
}

func (platform *fakePlatform) Snapshot() (Snapshot, error) {
	return platform.snapshot, platform.snapshotErr
}

type fakeCloser struct {
	closed bool
	err    error
}

func (closer *fakeCloser) Close() error {
	closer.closed = true
	return closer.err
}
