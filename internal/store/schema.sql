PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS canonical_messages (
    canonical_id TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL,
    tombstoned_at INTEGER
);

CREATE TABLE IF NOT EXISTS message_copies (
    canonical_id TEXT NOT NULL REFERENCES canonical_messages(canonical_id) ON DELETE CASCADE,
    endpoint_id TEXT NOT NULL,
    remote_message_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    from_self BOOLEAN NOT NULL DEFAULT 0,
    PRIMARY KEY (endpoint_id, remote_message_id),
    UNIQUE (canonical_id, endpoint_id)
);
CREATE INDEX IF NOT EXISTS idx_message_copies_canonical ON message_copies(canonical_id);

CREATE TABLE IF NOT EXISTS reactions (
    canonical_id TEXT NOT NULL REFERENCES canonical_messages(canonical_id) ON DELETE CASCADE,
    source_endpoint_id TEXT NOT NULL,
    actor_hash TEXT NOT NULL,
    emoji TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (canonical_id, source_endpoint_id, actor_hash)
);

CREATE TABLE IF NOT EXISTS recovery_cursors (
    endpoint_id TEXT PRIMARY KEY,
    remote_message_id TEXT,
    message_timestamp INTEGER,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS schema_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
INSERT OR IGNORE INTO schema_meta(key, value) VALUES ('schema_version', '3');
