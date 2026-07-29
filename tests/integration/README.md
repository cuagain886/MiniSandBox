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
