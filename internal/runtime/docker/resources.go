package docker

import (
	"errors"
	"math"

	mobycontainer "github.com/moby/moby/api/types/container"
	"minisandbox/internal/domain"
)

const (
	nanoCPUsPerMilliCPU = int64(1_000_000)
	bytesPerMiB         = int64(1024 * 1024)
)

// convertResourceLimits 把领域单位转换为 Docker cgroup 资源单位。
//
// CPU 从毫核转为 NanoCPUs，内存从 MiB 转为字节，PIDs 保持计数；
// 非正数和乘法溢出一律拒绝，不能退化为 Docker 的“不限制”语义。
func convertResourceLimits(
	limits domain.ResourceLimits,
) (mobycontainer.Resources, error) {
	if limits.CPUQuotaMillis <= 0 ||
		limits.CPUQuotaMillis > math.MaxInt64/nanoCPUsPerMilliCPU {
		return mobycontainer.Resources{}, errors.New("CPU limit is invalid")
	}
	if limits.MemoryMiB <= 0 ||
		limits.MemoryMiB > math.MaxInt64/bytesPerMiB {
		return mobycontainer.Resources{}, errors.New("memory limit is invalid")
	}
	if limits.PIDs <= 0 {
		return mobycontainer.Resources{}, errors.New("PIDs limit is invalid")
	}

	pids := limits.PIDs
	return mobycontainer.Resources{
		NanoCPUs:  limits.CPUQuotaMillis * nanoCPUsPerMilliCPU,
		Memory:    limits.MemoryMiB * bytesPerMiB,
		PidsLimit: &pids,
	}, nil
}
