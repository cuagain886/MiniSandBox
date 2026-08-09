package egressanchor

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/egressnft"
	"minisandbox/internal/egresspolicy"
)

// TestPrepareAndVerifyInMemory 验证新控制通道可以在不关闭 stdin、不写文件的前提下
// 完成降权并生成内存证明，后续 inspect 会重新回读权限并拒绝漂移。
func TestPrepareAndVerifyInMemory(t *testing.T) {
	bootstrap := testBootstrap(t)
	platform := validPlatform(bootstrap)
	attestation, err := Prepare(bootstrap, platform, func() time.Time {
		return time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("prepare anchor: %v", err)
	}
	if platform.dropUID != bootstrap.AnchorUID || platform.dropGID != bootstrap.AnchorGID ||
		attestation.PolicyHash != bootstrap.Policy.Hash {
		t.Fatalf("unexpected in-memory attestation: platform=%+v attestation=%+v", platform, attestation)
	}
	if err := Verify(platform, attestation, bootstrap.AnchorUID, bootstrap.AnchorGID); err != nil {
		t.Fatalf("verify stable anchor: %v", err)
	}
	platform.snapshot.CapEffective = uint64(1) << 12
	if err := Verify(platform, attestation, bootstrap.AnchorUID, bootstrap.AnchorGID); err == nil {
		t.Fatal("capability drift was accepted")
	}
}

// TestPrepareFailClosed 验证无效 bootstrap、netns 漂移、降权失败及权限回验失败均
// 不会生成可返回的内存 attestation。
func TestPrepareFailClosed(t *testing.T) {
	bootstrap := testBootstrap(t)
	invalidBootstrap := bootstrap
	invalidBootstrap.AnchorUID = 0
	invalidPlatform := validPlatform(bootstrap)
	if _, err := Prepare(invalidBootstrap, invalidPlatform, nil); err == nil || invalidPlatform.dropUID != 0 {
		t.Fatal("invalid bootstrap reached irreversible privilege drop")
	}
	const netAdmin = uint64(1) << 12
	tests := []struct {
		name     string
		platform *fakePlatform
	}{
		{name: "netns lookup failure", platform: &fakePlatform{networkErr: errors.New("stat")}},
		{name: "netns mismatch", platform: &fakePlatform{networkNamespace: "linux-netns:1:2"}},
		{name: "drop failure", platform: &fakePlatform{networkNamespace: bootstrap.NetworkNamespace, dropErr: errors.New("capset")}},
		{name: "snapshot failure", platform: &fakePlatform{networkNamespace: bootstrap.NetworkNamespace, snapshotErr: errors.New("status")}},
		{name: "wrong uid", platform: &fakePlatform{networkNamespace: bootstrap.NetworkNamespace, snapshot: Snapshot{UID: 1, GID: bootstrap.AnchorGID, NoNewPrivileges: true}}},
		{name: "additional supplementary group", platform: &fakePlatform{networkNamespace: bootstrap.NetworkNamespace, snapshot: Snapshot{UID: bootstrap.AnchorUID, GID: bootstrap.AnchorGID, SupplementaryGroups: []uint32{bootstrap.AnchorGID, 10}, NoNewPrivileges: true}}},
		{name: "effective NET_ADMIN", platform: &fakePlatform{networkNamespace: bootstrap.NetworkNamespace, snapshot: Snapshot{UID: bootstrap.AnchorUID, GID: bootstrap.AnchorGID, CapEffective: netAdmin, NoNewPrivileges: true}}},
		{name: "permitted NET_ADMIN", platform: &fakePlatform{networkNamespace: bootstrap.NetworkNamespace, snapshot: Snapshot{UID: bootstrap.AnchorUID, GID: bootstrap.AnchorGID, CapPermitted: netAdmin, NoNewPrivileges: true}}},
		{name: "ambient NET_ADMIN", platform: &fakePlatform{networkNamespace: bootstrap.NetworkNamespace, snapshot: Snapshot{UID: bootstrap.AnchorUID, GID: bootstrap.AnchorGID, CapAmbient: netAdmin, NoNewPrivileges: true}}},
		{name: "no_new_privs disabled", platform: &fakePlatform{networkNamespace: bootstrap.NetworkNamespace, snapshot: Snapshot{UID: bootstrap.AnchorUID, GID: bootstrap.AnchorGID}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if attestation, err := Prepare(bootstrap, test.platform, nil); err == nil || attestation != (Attestation{}) {
				t.Fatalf("failed anchor declared Ready: attestation=%+v err=%v", attestation, err)
			}
		})
	}
}

// TestVerifySnapshotAllowsOnlyPrimarySupplementaryGroup 验证 Docker/runc 重复主 GID
// 不被误判为额外权限，同时任何不同 GID 都继续 fail closed。
func TestVerifySnapshotAllowsOnlyPrimarySupplementaryGroup(t *testing.T) {
	const uid, gid = uint32(65532), uint32(65532)
	for _, groups := range [][]uint32{nil, {gid}, {gid, gid}} {
		if err := verifySnapshot(Snapshot{UID: uid, GID: gid, SupplementaryGroups: groups, NoNewPrivileges: true}, uid, gid); err != nil {
			t.Fatalf("equivalent primary groups rejected: groups=%v err=%v", groups, err)
		}
	}
	if err := verifySnapshot(Snapshot{UID: uid, GID: gid, SupplementaryGroups: []uint32{gid, 1000}, NoNewPrivileges: true}, uid, gid); err == nil {
		t.Fatal("additional group privilege was accepted")
	}
}

// TestAttestationValidation 验证 attach payload 的大小、封闭 schema、重复字段和版本。
func TestAttestationValidation(t *testing.T) {
	bootstrap := testBootstrap(t)
	valid := Attestation{
		ProtocolVersion: bootstrap.Policy.ProtocolVersion, RuleSchemaVersion: bootstrap.Policy.RuleSchemaVersion,
		PolicyHash: bootstrap.Policy.Hash, NetworkNamespace: bootstrap.NetworkNamespace,
		ImageDigest: bootstrap.ImageDigest, CreatedAt: time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC),
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal attestation: %v", err)
	}
	if _, err := ParseAttestation(encoded); err != nil {
		t.Fatalf("parse valid attestation: %v", err)
	}

	tests := []struct {
		name    string
		content string
	}{
		{name: "unknown field", content: strings.TrimSuffix(string(encoded), "}") + `,"unknown":true}`},
		{name: "duplicate field", content: strings.TrimSuffix(string(encoded), "}") + `,"policy_hash":"` + valid.PolicyHash + `"}`},
		{name: "trailing JSON", content: string(encoded) + `{}`},
		{name: "oversized", content: strings.Repeat("x", MaxAttestationBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseAttestation([]byte(test.content)); err == nil {
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
		UID: bootstrap.AnchorUID, GID: bootstrap.AnchorGID, SupplementaryGroups: []uint32{bootstrap.AnchorGID}, NoNewPrivileges: true,
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
