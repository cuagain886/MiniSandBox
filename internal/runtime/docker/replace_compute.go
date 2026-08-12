package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"minisandbox/internal/domain"
	"minisandbox/internal/runnerbootstrap"
	runtimeport "minisandbox/internal/runtime"
)

const runnerCredentialFileName = "runner-token"

// ReplaceCompute 只替换 sandbox 的计算成员并保留 workspace volume 与 lease.json。
func (r *Runtime) ReplaceCompute(ctx context.Context, sandbox domain.Sandbox) (runtimeport.ActualSandbox, error) {
	if _, err := r.validateEnsureInput(sandbox); err != nil {
		return runtimeport.ActualSandbox{}, err
	}
	if err := r.removeComputeResources(ctx, sandbox.ID); err != nil {
		return runtimeport.ActualSandbox{}, err
	}
	if err := r.clearComputeRuntimeFiles(sandbox.ID); err != nil {
		return runtimeport.ActualSandbox{}, err
	}
	return r.Ensure(ctx, sandbox)
}

func (r *Runtime) removeComputeResources(ctx context.Context, sandboxID string) error {
	// main 必须先离开 sidecar 持有的 namespace，随后才能删除 namespace anchor。
	if err := deleteManagedContainer(ctx, r.engine, sandboxID, defaultContainerStopTimeout); err != nil {
		return fmt.Errorf("replace main container: %w", err)
	}
	if engine, ok := r.engine.(EgressEngine); ok {
		if err := deleteManagedEgressSidecar(ctx, engine, sandboxID, defaultContainerStopTimeout); err != nil {
			return fmt.Errorf("replace egress sidecar: %w", err)
		}
	}
	return nil
}

func (r *Runtime) clearComputeRuntimeFiles(sandboxID string) error {
	names, err := NamesForSandbox(r.dataDirectory, sandboxID)
	if err != nil {
		return err
	}
	if err := requireRealDirectory(names.RuntimeDirectory); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("validate compute runtime directory: %w", err)
	}
	// 只删除协议规定的临时对象；lease.json 不在 allowlist 中，因此恢复不会改变当前租约事实。
	targets := []string{
		names.HostRunnerSocket,
		filepath.Join(names.RuntimeDirectory, runnerbootstrap.ConfigFileName),
		filepath.Join(names.RuntimeDirectory, runnerCredentialFileName),
		filepath.Join(names.RuntimeDirectory, "executions"),
	}
	for _, target := range targets {
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("remove compute runtime temporary data: %w", err)
		}
	}
	bootstrapTemps, err := filepath.Glob(filepath.Join(names.RuntimeDirectory, ".bootstrap.json.tmp-*"))
	if err != nil {
		return fmt.Errorf("enumerate bootstrap temporary data: %w", err)
	}
	for _, target := range bootstrapTemps {
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove bootstrap temporary data: %w", err)
		}
	}
	return nil
}

var _ runtimeport.ComputeReplacer = (*Runtime)(nil)
