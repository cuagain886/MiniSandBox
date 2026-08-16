package docker

import (
	"context"
	"time"
)

// PrepareImage 确保指定镜像在本机可用，缺失时执行后台预拉取语义的拉取。
//
// 该方法与 sandbox 创建共用同一条 ensureImage 路径（inspect→pull→
// inspect）和 pull limiter；启动预拉取因此不会绕过镜像 allowlist 或
// 并发上限。本方法不创建任何 sandbox 资源。
func (r *Runtime) PrepareImage(ctx context.Context, image string) error {
	_, err := ensureImage(ctx, r.engine, image, r.createTimeout, r.imagePullLimiter)
	return err
}

// PrePullSequentialTimeout 是顺序准备多个镜像时单个镜像的兜底超时。
const PrePullSequentialTimeout = 10 * time.Minute
