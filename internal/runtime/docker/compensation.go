package docker

import (
	"context"
	"errors"
	"fmt"
)

// ensureJournal 只记录一次 Ensure 调用实际新建的受管资源。
//
// journal 不记录镜像，也不根据“资源当前存在”推测归属；这能避免失败补偿
// 删除调用前已经存在、只是被本次 Ensure 复用的资源。
type ensureJournal struct {
	sandboxID        string
	directoryCreated bool
	volumeCreated    bool
	containerCreated bool
}

// compensate 按创建顺序的逆序 best-effort 清理本次调用产生的副作用。
//
// 每一步失败后仍继续后续清理，最终用 errors.Join 保留全部内部 cause。
func (j ensureJournal) compensate(ctx context.Context, runtime *Runtime) error {
	var failures []error
	if j.containerCreated {
		if err := deleteManagedContainer(
			ctx,
			runtime.engine,
			j.sandboxID,
			defaultContainerStopTimeout,
		); err != nil {
			failures = append(failures, fmt.Errorf(
				"compensate container: %w",
				err,
			))
		}
	}
	if j.volumeCreated {
		if err := deleteWorkspaceVolume(
			ctx,
			runtime.engine,
			j.sandboxID,
		); err != nil {
			failures = append(failures, fmt.Errorf(
				"compensate workspace volume: %w",
				err,
			))
		}
	}
	if j.directoryCreated {
		if err := DeleteRuntimeDirectory(
			runtime.dataDirectory,
			j.sandboxID,
		); err != nil {
			failures = append(failures, fmt.Errorf(
				"compensate runtime directory: %w",
				err,
			))
		}
	}
	return errors.Join(failures...)
}

// ensureFailure 保留原始失败；补偿未完成时将其升级为 cleanup pending。
func ensureFailure(
	ctx context.Context,
	runtime *Runtime,
	journal ensureJournal,
	operationErr error,
) error {
	cleanupErr := journal.compensate(ctx, runtime)
	if cleanupErr == nil {
		return operationErr
	}
	return &CleanupPendingError{
		cause:        cleanupErr,
		operationErr: operationErr,
	}
}
