package docker

import (
	"context"
	"errors"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	mobyvolume "github.com/moby/moby/api/types/volume"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/domain"
)

// TestDeleteWorkspaceVolumeMissingIsSuccess 验证不存在时不调用 remove。
func TestDeleteWorkspaceVolumeMissingIsSuccess(t *testing.T) {
	removeCalls := 0
	engine := &fakeEngine{
		volumeInspectFunc: func(
			context.Context,
			string,
			mobyclient.VolumeInspectOptions,
		) (mobyclient.VolumeInspectResult, error) {
			return mobyclient.VolumeInspectResult{}, cerrdefs.ErrNotFound
		},
		volumeRemoveFunc: func(
			context.Context,
			string,
			mobyclient.VolumeRemoveOptions,
		) (mobyclient.VolumeRemoveResult, error) {
			removeCalls++
			return mobyclient.VolumeRemoveResult{}, nil
		},
	}

	if err := deleteWorkspaceVolume(
		context.Background(),
		engine,
		testSandboxID,
	); err != nil {
		t.Fatalf("delete missing volume: %v", err)
	}
	if removeCalls != 0 {
		t.Fatalf("remove calls: got %d, want 0", removeCalls)
	}
}

// TestDeleteWorkspaceVolumeSuccessNeverForces 验证身份匹配时只使用非 force 删除。
func TestDeleteWorkspaceVolumeSuccessNeverForces(t *testing.T) {
	engine := &fakeEngine{
		volumeInspectFunc: func(
			context.Context,
			string,
			mobyclient.VolumeInspectOptions,
		) (mobyclient.VolumeInspectResult, error) {
			return matchingVolumeInspection(t), nil
		},
		volumeRemoveFunc: func(
			_ context.Context,
			name string,
			options mobyclient.VolumeRemoveOptions,
		) (mobyclient.VolumeRemoveResult, error) {
			if name != testWorkspace || options.Force {
				t.Fatalf("remove request: name=%q options=%#v", name, options)
			}
			return mobyclient.VolumeRemoveResult{}, nil
		},
	}

	if err := deleteWorkspaceVolume(
		context.Background(),
		engine,
		testSandboxID,
	); err != nil {
		t.Fatalf("delete volume: %v", err)
	}
}

// TestDeleteWorkspaceVolumeRejectsLabelMismatch 验证同名非受管 volume 不被删除。
func TestDeleteWorkspaceVolumeRejectsLabelMismatch(t *testing.T) {
	removeCalls := 0
	inspection := matchingVolumeInspection(t)
	inspection.Volume.Labels = map[string]string{}
	engine := &fakeEngine{
		volumeInspectFunc: func(
			context.Context,
			string,
			mobyclient.VolumeInspectOptions,
		) (mobyclient.VolumeInspectResult, error) {
			return inspection, nil
		},
		volumeRemoveFunc: func(
			context.Context,
			string,
			mobyclient.VolumeRemoveOptions,
		) (mobyclient.VolumeRemoveResult, error) {
			removeCalls++
			return mobyclient.VolumeRemoveResult{}, nil
		},
	}

	err := deleteWorkspaceVolume(context.Background(), engine, testSandboxID)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error: got %v, want conflict", err)
	}
	if removeCalls != 0 {
		t.Fatalf("remove calls: got %d, want 0", removeCalls)
	}
}

// TestDeleteWorkspaceVolumeInUseIsCleanupPending 验证占用冲突可安全重试且不泄露详情。
func TestDeleteWorkspaceVolumeInUseIsCleanupPending(t *testing.T) {
	cause := cerrdefs.ErrConflict
	engine := &fakeEngine{
		volumeInspectFunc: func(
			context.Context,
			string,
			mobyclient.VolumeInspectOptions,
		) (mobyclient.VolumeInspectResult, error) {
			return matchingVolumeInspection(t), nil
		},
		volumeRemoveFunc: func(
			context.Context,
			string,
			mobyclient.VolumeRemoveOptions,
		) (mobyclient.VolumeRemoveResult, error) {
			return mobyclient.VolumeRemoveResult{}, cause
		},
	}

	err := deleteWorkspaceVolume(context.Background(), engine, testSandboxID)
	var pending *CleanupPendingError
	if !errors.As(err, &pending) ||
		!pending.CleanupPending() ||
		!errors.Is(err, cause) {
		t.Fatalf("error: got %T %v, want cleanup pending", err, err)
	}
	if strings.Contains(err.Error(), testWorkspace) {
		t.Fatal("cleanup pending error exposed volume name")
	}
}

// matchingVolumeInspection 返回身份匹配的 workspace volume inspect 结果。
func matchingVolumeInspection(t *testing.T) mobyclient.VolumeInspectResult {
	t.Helper()
	return mobyclient.VolumeInspectResult{
		Volume: mobyvolume.Volume{
			Name:   testWorkspace,
			Labels: validTestLabels(t),
		},
	}
}
