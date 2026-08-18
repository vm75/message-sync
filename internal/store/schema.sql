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

CREATE TABLE IF NOT EXISTS global_config (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    username_mode TEXT NOT NULL DEFAULT 'push_name',
    media_enabled BOOLEAN NOT NULL DEFAULT 1,
    media_max_size_mb INTEGER NOT NULL DEFAULT 100,
    recovery_enabled BOOLEAN NOT NULL DEFAULT 1,
    recovery_max_age_hours INTEGER NOT NULL DEFAULT 24,
    recovery_max_messages_per_group INTEGER NOT NULL DEFAULT 200,
    storage_message_retention_days INTEGER NOT NULL DEFAULT 90,
    admin_password_hash TEXT NOT NULL DEFAULT '',
    poll_aggregation_trigger TEXT NOT NULL DEFAULT 'aggregate-response'
);
INSERT OR IGNORE INTO global_config (id) VALUES (1);

CREATE TABLE IF NOT EXISTS sync_sets (
    id TEXT PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS groups (
    alias TEXT PRIMARY KEY,
    jid TEXT NOT NULL UNIQUE,
    sync_set_id TEXT REFERENCES sync_sets(id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_jid ON groups(jid);

CREATE TABLE IF NOT EXISTS poll_options (
    canonical_id TEXT NOT NULL REFERENCES canonical_messages(canonical_id) ON DELETE CASCADE,
    option_index INTEGER NOT NULL,
    option_hash TEXT NOT NULL,
    PRIMARY KEY (canonical_id, option_index)
);
CREATE INDEX IF NOT EXISTS idx_poll_options_canonical ON poll_options(canonical_id);

CREATE TABLE IF NOT EXISTS poll_votes (
    canonical_id TEXT NOT NULL REFERENCES canonical_messages(canonical_id) ON DELETE CASCADE,
    endpoint_id TEXT NOT NULL,
    actor_hash TEXT NOT NULL,
    option_hash TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (canonical_id, endpoint_id, actor_hash, option_hash)
);
CREATE INDEX IF NOT EXISTS idx_poll_votes_canonical ON poll_votes(canonical_id);

CREATE TABLE IF NOT EXISTS schema_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
INSERT OR IGNORE INTO schema_meta(key, value) VALUES ('schema_version', '8');
