# Integration tests

Docker 集成测试默认不会运行，必须在 Linux Docker 主机显式 opt-in：

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
go build -o internal/embedded/artifacts/linux_amd64/runnerd ./cmd/runnerd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
go build -o internal/embedded/artifacts/linux_amd64/sandbox-init ./cmd/sandbox-init

MINISANDBOX_INTEGRATION=1 \
go test -tags=integration ./tests/integration/...
```

非默认 Docker socket 可通过 `MINISANDBOX_TEST_DOCKER_HOST` 指定。Harness
为每个测试生成独立 data directory 和随机
`io.minisandbox.integration-test-id` label，cleanup 只枚举当前 test ID，
禁止按 `minisandbox-*` 名称前缀清理。

Docker Desktop WSL 等需要显式共享宿主目录的环境，可通过
`MINISANDBOX_TEST_DATA_ROOT` 指定 daemon 与测试进程都能访问的绝对路径。
该路径应尽量短，因为 Linux Unix Socket 完整路径不能超过 107 字节；Harness
会在此根目录下创建随机测试子目录，并在 Docker 资源清理完成后删除该子目录。

当前 Phase 1 integration suite 覆盖：

- harness 隔离与精确清理；
- create 最终进入 Running；
- 重复 Ensure 不产生重复资源；
- 创建失败后的 container、volume 和 runtime directory 补偿；
- cleanup pending 经显式 DELETE 恢复；
- 重复 DELETE 与外部 container 缺失；
- sandboxd 重启后复用既有 container；
- Docker inspect 安全配置；
- container/volume labels allowlist 与秘密排除；
- 每个 sandbox 独立 Unix Socket 的路径、inode、权限和删除隔离。
