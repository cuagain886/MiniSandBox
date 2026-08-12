package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
	"minisandbox/internal/store"
)

const (
	runnerHealthFailureThreshold = 3
	runningShutdownTimeout       = 10 * time.Second
)

func (r *Reconciler) recoverRunning(ctx context.Context, sandbox domain.Sandbox, replacer runtimeport.ComputeReplacer) error {
	if err := r.projectLease(sandbox); err != nil {
		return r.recordFailure(ctx, sandbox, err, sandbox.RuntimeID, RetryOperationRecover)
	}
	actual, err := r.runtime.Inspect(ctx, sandbox.ID)
	if err != nil {
		return r.recordFailure(ctx, sandbox, err, sandbox.RuntimeID, RetryOperationRecover)
	}
	if actual.State != runtimeport.ActualRunning || actual.SpecHash != sandbox.SpecHash {
		if sandbox.Spec.Network.Outbound {
			return r.replaceRunningCompute(ctx, sandbox, replacer)
		}
		actual, err = r.ensureRuntime(ctx, sandbox)
		if err != nil {
			return r.recordFailure(ctx, sandbox, err, sandbox.RuntimeID, RetryOperationRecover)
		}
	}
	if sandbox.Spec.Network.Outbound {
		networkProbe, probeOK := r.probe.(RunnerNetworkProbe)
		egressGate, gateOK := r.runtime.(runtimeport.ExecutionEgressGate)
		if !probeOK || !gateOK {
			return r.replaceRunningCompute(ctx, sandbox, replacer)
		}
		identity, probeErr := networkProbe.ProbeNetwork(ctx, sandbox.ID, actual.RunnerProtocolVersion)
		if probeErr != nil {
			return r.handleRunningProbeFailure(ctx, sandbox, replacer, probeErr)
		}
		if err := egressGate.CheckSandboxEgress(ctx, sandbox.ID, identity); err != nil {
			// egress attestation、policy、netns 或服务网络失败都会立即关闭准入并聚合替换。
			return r.replaceRunningCompute(ctx, sandbox, replacer)
		}
	} else if err := r.probe.Probe(ctx, sandbox.ID, actual.RunnerProtocolVersion); err != nil {
		return r.handleRunningProbeFailure(ctx, sandbox, replacer, err)
	}
	return r.recordRunningHealth(ctx, sandbox, true)
}

func (r *Reconciler) handleRunningProbeFailure(ctx context.Context, sandbox domain.Sandbox, replacer runtimeport.ComputeReplacer, cause error) error {
	updated, err := r.store.RecordHealthResult(ctx, store.HealthResultUpdate{
		ID: sandbox.ID, ExpectedRevision: sandbox.Revision, CheckedAt: r.clock.Now().UTC(), Healthy: false,
	})
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return r.honorConcurrentDelete(ctx, sandbox.ID)
		}
		return fmt.Errorf("record runner health failure: %w", err)
	}
	if updated.HealthFailureCount < runnerHealthFailureThreshold {
		return nil
	}
	if err := r.replaceRunningCompute(ctx, updated, replacer); err != nil {
		return errors.Join(cause, err)
	}
	return nil
}

func (r *Reconciler) replaceRunningCompute(ctx context.Context, sandbox domain.Sandbox, replacer runtimeport.ComputeReplacer) error {
	if r.shutdown != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, runningShutdownTimeout)
		_ = r.shutdown.Shutdown(shutdownCtx, sandbox.ID)
		cancel()
	}
	current, err := r.store.Get(ctx, sandbox.ID)
	if err != nil {
		return fmt.Errorf("recheck sandbox before compute replacement: %w", err)
	}
	if current.DesiredState == domain.DesiredTerminated {
		return r.reconcileTerminated(ctx, current)
	}
	actual, err := replacer.ReplaceCompute(ctx, current)
	if err != nil {
		return r.recordFailure(ctx, current, err, current.RuntimeID, RetryOperationRecover)
	}
	if current.Spec.Network.Outbound {
		networkProbe, probeOK := r.probe.(RunnerNetworkProbe)
		egressGate, gateOK := r.runtime.(runtimeport.ExecutionEgressGate)
		if !probeOK || !gateOK {
			return errors.New("outbound recovery readiness gate is unavailable")
		}
		identity, err := networkProbe.ProbeNetwork(ctx, current.ID, actual.RunnerProtocolVersion)
		if err != nil {
			return err
		}
		if err := egressGate.CheckSandboxEgress(ctx, current.ID, identity); err != nil {
			return err
		}
	} else if err := r.probe.Probe(ctx, current.ID, actual.RunnerProtocolVersion); err != nil {
		return err
	}
	return r.recordRunningHealth(ctx, current, true)
}

func (r *Reconciler) recordRunningHealth(ctx context.Context, sandbox domain.Sandbox, healthy bool) error {
	updated, err := r.store.RecordHealthResult(ctx, store.HealthResultUpdate{
		ID: sandbox.ID, ExpectedRevision: sandbox.Revision, CheckedAt: r.clock.Now().UTC(), Healthy: healthy,
	})
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return r.honorConcurrentDelete(ctx, sandbox.ID)
		}
		return fmt.Errorf("record runner health result: %w", err)
	}
	return r.projectLease(updated)
}

func (r *Reconciler) honorConcurrentDelete(ctx context.Context, sandboxID string) error {
	current, err := r.store.Get(ctx, sandboxID)
	if err != nil {
		return err
	}
	if current.DesiredState == domain.DesiredTerminated {
		return r.reconcileTerminated(ctx, current)
	}
	return domain.ErrConflict
}
