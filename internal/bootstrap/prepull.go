package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"minisandbox/internal/config"
	runtimeport "minisandbox/internal/runtime"
)

// runImagePrePull 在后台顺序准备配置中的常用镜像。
//
// 预拉取不阻塞 readiness，也不影响任何 sandbox 生命周期；单个镜像使用
// 与创建路径一致的校验与拉取通道。失败只记录结构化日志并在进程重启后
// 按配置重试，Docker 本地缓存是唯一事实源。
func runImagePrePull(runtime runtimeport.Runtime, images []config.PrePullImage, timeout time.Duration) {
	if len(images) == 0 {
		return
	}
	preparer, ok := runtime.(runtimeport.ImagePreparer)
	if !ok {
		slog.Warn("runtime does not support image pre-pull")
		return
	}
	if timeout <= 0 {
		timeout = time.Minute
	}
	go func() {
		for _, entry := range images {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			err := preparer.PrepareImage(ctx, entry.Image, entry.Platform)
			cancel()
			if err != nil {
				slog.Warn("image pre-pull failed", "image", entry.Image, "platform", entry.Platform, "error_type", errTypeName(err))
				continue
			}
			slog.Info("image pre-pull ready", "image", entry.Image, "platform", entry.Platform)
		}
	}()
}

// errTypeName 返回错误的具体类型名。
//
// 只记录类型不记录文本，避免 registry 响应细节进入日志。
func errTypeName(err error) string {
	return fmt.Sprintf("%T", err)
}
