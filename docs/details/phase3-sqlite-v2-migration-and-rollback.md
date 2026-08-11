# Phase 3 SQLite v2 迁移与回滚边界

P3-010 把 Phase 2 schema v1 升级为 v2，只增加 sandbox lease、retry、health
和 origin 字段。v3 idempotency table 与 v4 anomaly table 不属于本次迁移。

## 自动升级保证

- 已存在的 v1 数据库先通过 SQLite `VACUUM INTO` 在原目录生成一致性备份，
  文件名形如 `sandboxd.db.pre-v1.<utc-unix-nano>.<sequence>.bak`；备份失败时
  不执行任何 DDL，也不更新 schema version。
- migration 使用 `BEGIN IMMEDIATE`。建表、逐行回填、旧表替换、索引重建和
  version 记录处于同一事务，任一步失败都回滚到完整 v1。
- migration 前检查 v1 列、版本和 `idx_sandboxes_reconcile`，提交前后检查 v2
  列、版本和同一关键索引；未知更高版本拒绝启动。
- v1 `DesiredRunning` 的 `expires_at` 使用一次 migration clock 加 30 分钟；
  `DesiredTerminated` 使用 migration clock；已 observed `Terminated` 优先使用
  原 `last_transition_at`。retry、health 清零，nullable 调度时间为空，origin
  回填为 `api`。

备份是回滚边界，不是持续备份策略。迁移成功后不得自动删除或覆盖它。

## 允许恢复 v1 备份的唯一窗口

只有同时满足以下条件，才允许停机恢复 `.pre-v1.*.bak` 并重新运行 Phase 2：

1. Phase 3 二进制尚未对任何 sandbox 执行 create、renew、expire、retry、
   health update、trusted orphan import、recovery 或 runtime reconcile；
2. 尚未创建 v3/v4 数据，也没有写入新的 lease projection 或 v2 Docker label；
3. sandboxd 已完全停止，数据库及其 WAL/SHM 没有活跃连接；
4. 选中的备份通过 SQLite integrity check、schema version=1、列和关键索引校验。

恢复前必须保留当前 v2 数据库及 WAL/SHM 作为故障证据，在同一文件系统内以
原子替换方式恢复已验证备份，然后再由 Phase 2 二进制只读检查和启动。具体
文件操作应由运维 runbook 在停机窗口执行，应用程序不提供自动 down migration。

一旦产生任何 Phase 3 Store 或 runtime 副作用，v1 备份已经不能表达最新权威
状态；此时禁止回退旧库或用 Phase 2 二进制打开 v2，只能 forward-fix，或先
执行受控 drain、确认所有受管资源终止后再按新的迁移方案处理。
