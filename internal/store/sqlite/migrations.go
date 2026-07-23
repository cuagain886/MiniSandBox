package sqlite

const initialSchema = `
CREATE TABLE IF NOT EXISTS sandboxes (
    id TEXT PRIMARY KEY,
    desired_state TEXT NOT NULL,
    observed_state TEXT NOT NULL,
    spec_json BLOB NOT NULL,
    revision INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    expires_at TEXT,
    failure_reason TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS idempotency_keys (
    key TEXT PRIMARY KEY,
    sandbox_id TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    created_at TEXT NOT NULL
);
`
