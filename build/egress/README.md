# Egress sidecar artifact

该目录固定 Phase 2 egress namespace anchor 的构建输入。最终镜像以 `scratch` 为根，
只复制静态 `egressd`、固定版本 `nft` 及其动态链接库；不包含 shell、包管理器、
runner、Docker CLI、代理或凭据组件。

构建必须使用 linux/amd64、精确输出名称和 BuildKit 的 SPDX SBOM/provenance：

```bash
make egress-image EGRESS_IMAGE=registry.example/minisandbox/egressd:build
```

`dist/egress-build-metadata.json` 是候选构建元数据，不是批准记录。发布流程必须从该
结果取得 image digest、SBOM digest 和 provenance digest，与源码 revision、
`artifact-contract.json` 一同进入 release manifest；生产配置只接受最终
`name@sha256:<64 lowercase hex>`，不得使用这里的临时 tag。

运行时必须使用只读 rootfs，不挂载 tmpfs、bind mount 或 volume。`sandboxd` 通过
`OpenStdin=true`、`StdinOnce=false` 的 Docker attach stdin/stdout 完成唯一 bootstrap
和后续只读 inspect；attestation 只保存在 `egressd` 内存中，sidecar 固定使用
`log-driver=none`。固定 entrypoint 不允许覆盖，镜像本身不声明 healthcheck、端口、
volume 或额外命令。
