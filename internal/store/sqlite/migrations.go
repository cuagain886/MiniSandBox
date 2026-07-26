package sqlite

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// migration 描述一次只向前的 schema 变更。
type migration struct {
	// version 是严格递增的迁移版本号，从 1 开始。
	version int64
	// statements 是本版本按顺序执行的 DDL 语句。
	statements []string
}

// migrations 是全部已知迁移，追加新版本时必须保持版本严格递增。
//
// migration 1 创建计划 5.4 节要求的 sandboxes 表：spec_json 保存 resolved
// spec，revision 支撑 CAS 更新，时间字段使用 UTC RFC3339Nano 文本。索引
// (desired_state, observed_state) 服务于 reconcile candidate 扫描，避免随
// 记录增长全表遍历。Phase 1 不创建 idempotency 相关表。
var migrations = []migration{
	{
		version: 1,
		statements: []string{
			`CREATE TABLE sandboxes (
				id TEXT PRIMARY KEY,
				spec_json BLOB NOT NULL,
				desired_state TEXT NOT NULL,
				observed_state TEXT NOT NULL,
				reason TEXT NOT NULL,
				message TEXT NOT NULL,
				runtime_id TEXT NOT NULL DEFAULT '',
				spec_hash TEXT NOT NULL,
				revision INTEGER NOT NULL,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL,
				last_transition_at TEXT NOT NULL
			)`,
			`CREATE INDEX idx_sandboxes_reconcile
				ON sandboxes (desired_state, observed_state)`,
		},
	},
}

// Migrate 在单个事务内把数据库 schema 迁移到最新已知版本。
//
// 版本表与全部待执行迁移共用一个事务：任何一步失败都整体回滚，不留下
// 半完成的 schema。同一版本只会执行一次；数据库版本高于本二进制已知的
// 最高版本时拒绝继续，避免旧程序破坏新 schema。重复调用幂等。
func (s *Store) Migrate(ctx context.Context) error {
	return s.migrateWith(ctx, migrations)
}

// migrateWith 用给定迁移列表执行迁移，供 Migrate 和失败回滚测试复用。
func (s *Store) migrateWith(ctx context.Context, list []migration) error {
	if err := validateMigrationList(list); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	// commit 成功后 Rollback 返回 ErrTxDone，是安全的空操作。
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(
		ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`,
	); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	var current int64
	if err := tx.QueryRowContext(
		ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&current); err != nil {
		return fmt.Errorf("read current schema version: %w", err)
	}

	latest := int64(0)
	if len(list) > 0 {
		latest = list[len(list)-1].version
	}
	if current > latest {
		return fmt.Errorf(
			"database schema version %d is newer than supported version %d",
			current,
			latest,
		)
	}

	for _, m := range list {
		if m.version <= current {
			continue
		}
		for _, statement := range m.statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply migration %d: %w", m.version, err)
			}
		}
		if _, err := tx.ExecContext(
			ctx,
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			m.version,
			time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

// validateMigrationList 校验迁移列表从 1 开始且版本严格递增。
//
// 列表由代码维护，乱序或跳号属于程序缺陷，必须在执行任何 DDL 前拒绝。
func validateMigrationList(list []migration) error {
	expected := int64(1)
	for _, m := range list {
		if m.version != expected {
			return errors.New(
				"migration list must start at 1 and increase strictly",
			)
		}
		expected++
	}
	return nil
}
