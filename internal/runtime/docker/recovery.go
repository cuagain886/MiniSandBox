package docker

import (
	"context"
	"errors"
	"path/filepath"

	"minisandbox/internal/egresspolicy"
	runtimeport "minisandbox/internal/runtime"
)

// InventoryRuntimeDirectories 从 Runtime 自身受管 data root 枚举目录，调用方不能注入任意宿主机路径。
func (r *Runtime) InventoryRuntimeDirectories(context.Context) ([]runtimeport.RuntimeDirectoryObservation, error) {
	return runtimeport.InventoryRuntimeDirectories(filepath.Join(r.dataDirectory, runtimeRootName))
}

// EnsureRecoveryEgressNetwork 在启用 outbound 时先幂等保证服务级 bridge；已有漂移网络会 fail closed。
func (r *Runtime) EnsureRecoveryEgressNetwork(ctx context.Context) error {
	if r.egressConfig == nil {
		return nil
	}
	engine, ok := r.engine.(EgressEngine)
	if !ok {
		return errors.New("docker engine does not support egress recovery")
	}
	_, err := ensureEgressNetwork(ctx, engine)
	return err
}

// RecoveryExpectation 从已验证 bridge IPAM 与受信 deny 配置计算 policy hash。
func (r *Runtime) RecoveryExpectation(ctx context.Context) (runtimeport.RecoveryExpectation, error) {
	if r.egressConfig == nil {
		return runtimeport.RecoveryExpectation{}, nil
	}
	engine, ok := r.engine.(EgressEngine)
	if !ok {
		return runtimeport.RecoveryExpectation{}, errors.New("docker engine does not support egress recovery")
	}
	network, err := ensureEgressNetwork(ctx, engine)
	if err != nil {
		return runtimeport.RecoveryExpectation{}, err
	}
	policy, err := egresspolicy.Build(r.egressConfig.AdditionalDeniedCIDRs, network.networks)
	if err != nil {
		return runtimeport.RecoveryExpectation{}, errors.New("build recovery egress policy")
	}
	return runtimeport.RecoveryExpectation{EgressPolicyHash: policy.Hash}, nil
}

var _ runtimeport.RecoveryInventory = (*Runtime)(nil)
var _ runtimeport.EgressRecoveryBootstrap = (*Runtime)(nil)
var _ runtimeport.RecoveryExpectationProvider = (*Runtime)(nil)
