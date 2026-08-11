CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);
INSERT INTO schema_migrations (version, applied_at)
VALUES (1, '2026-07-01T00:00:00Z');

CREATE TABLE sandboxes (
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
);
CREATE INDEX idx_sandboxes_reconcile
ON sandboxes (desired_state, observed_state);

INSERT INTO sandboxes (
    id, spec_json, desired_state, observed_state, reason, message,
    runtime_id, spec_hash, revision, created_at, updated_at, last_transition_at
) VALUES
(
    'phase2-deleting',
    '{"image":"alpine:3.22","resources":{"cpu_quota_millis":500,"memory_mib":256,"pids":64},"workspace":{"mount_path":"/workspace","persistent":false},"network":{"outbound":false},"platform":{"os":"linux","arch":"amd64"}}',
    'Terminated', 'Running', 'DELETE_ACCEPTED', 'Sandbox deletion has been accepted.',
    'runtime-delete', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 7,
    '2026-07-01T01:00:00Z', '2026-07-01T01:10:00Z', '2026-07-01T01:05:00Z'
),
(
    'phase2-running',
    '{"image":"alpine:3.22","resources":{"cpu_quota_millis":500,"memory_mib":256,"pids":64},"workspace":{"mount_path":"/workspace","persistent":false},"network":{"outbound":false},"platform":{"os":"linux","arch":"amd64"}}',
    'Running', 'Running', 'RUNNING', 'Sandbox is running.',
    'runtime-running', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 11,
    '2026-07-01T02:00:00Z', '2026-07-01T02:10:00Z', '2026-07-01T02:05:00Z'
),
(
    'phase2-terminated',
    '{"image":"alpine:3.22","resources":{"cpu_quota_millis":500,"memory_mib":256,"pids":64},"workspace":{"mount_path":"/workspace","persistent":false},"network":{"outbound":false},"platform":{"os":"linux","arch":"amd64"}}',
    'Terminated', 'Terminated', 'TERMINATED', 'Sandbox runtime has been deleted.',
    '', 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 13,
    '2026-07-01T03:00:00Z', '2026-07-01T03:10:00Z', '2026-07-01T03:05:00Z'
);
