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

-- One opaque native child scope may be retained per canonical message and
-- endpoint. Labels and provider objects remain transient at transport edges.
CREATE TABLE IF NOT EXISTS canonical_scopes (
    canonical_id TEXT NOT NULL REFERENCES canonical_messages(canonical_id) ON DELETE CASCADE,
    endpoint_id TEXT NOT NULL,
    scope_kind TEXT NOT NULL CHECK (scope_kind IN ('discord_thread', 'telegram_topic')),
    remote_scope_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (canonical_id, endpoint_id)
);
CREATE INDEX IF NOT EXISTS idx_canonical_scopes_canonical ON canonical_scopes(canonical_id);

CREATE TABLE IF NOT EXISTS reactions (
    canonical_id TEXT NOT NULL REFERENCES canonical_messages(canonical_id) ON DELETE CASCADE,
    source_endpoint_id TEXT NOT NULL,
    actor_hash TEXT NOT NULL,
    emoji TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (canonical_id, source_endpoint_id, actor_hash)
);

CREATE TABLE IF NOT EXISTS recovery_cursors (
    stream_key TEXT PRIMARY KEY,
    position INTEGER NOT NULL,
    event_timestamp INTEGER,
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
    poll_aggregation_trigger TEXT NOT NULL DEFAULT 'aggregate-response',
    whatsapp_chat_cleanup_enabled BOOLEAN NOT NULL DEFAULT 0,
    whatsapp_chat_retention_days INTEGER NOT NULL DEFAULT 30,
    local_message_prefix TEXT NOT NULL DEFAULT '',
    whatsapp_device_name TEXT NOT NULL DEFAULT 'message-sync'
);
INSERT OR IGNORE INTO global_config (id) VALUES (1);

CREATE TABLE IF NOT EXISTS sync_sets (
    id TEXT PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS endpoints (
    alias TEXT PRIMARY KEY,
    transport TEXT NOT NULL CHECK (transport IN ('whatsapp', 'discord', 'telegram')),
    remote_id TEXT NOT NULL,
    sync_set_id TEXT REFERENCES sync_sets(id) ON DELETE SET NULL,
    UNIQUE (transport, remote_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_endpoints_remote ON endpoints(transport, remote_id);

CREATE TABLE IF NOT EXISTS poll_options (
    canonical_id TEXT NOT NULL REFERENCES canonical_messages(canonical_id) ON DELETE CASCADE,
    option_index INTEGER NOT NULL,
    option_hash TEXT,
    PRIMARY KEY (canonical_id, option_index)
);
CREATE INDEX IF NOT EXISTS idx_poll_options_canonical ON poll_options(canonical_id);

CREATE TABLE IF NOT EXISTS poll_endpoint_sources (
    canonical_id TEXT NOT NULL REFERENCES canonical_messages(canonical_id) ON DELETE CASCADE,
    endpoint_id TEXT NOT NULL,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('actor', 'snapshot', 'unavailable')),
    PRIMARY KEY (canonical_id, endpoint_id)
);

CREATE TABLE IF NOT EXISTS poll_actor_selections (
    canonical_id TEXT NOT NULL REFERENCES canonical_messages(canonical_id) ON DELETE CASCADE,
    endpoint_id TEXT NOT NULL,
    actor_hash TEXT NOT NULL,
    option_index INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (canonical_id, endpoint_id, actor_hash, option_index)
);
CREATE INDEX IF NOT EXISTS idx_poll_actor_selections_canonical ON poll_actor_selections(canonical_id);

CREATE TABLE IF NOT EXISTS poll_endpoint_snapshots (
    canonical_id TEXT NOT NULL REFERENCES canonical_messages(canonical_id) ON DELETE CASCADE,
    endpoint_id TEXT NOT NULL,
    option_index INTEGER NOT NULL,
    option_count INTEGER NOT NULL CHECK (option_count >= 0),
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (canonical_id, endpoint_id, option_index)
);
CREATE INDEX IF NOT EXISTS idx_poll_endpoint_snapshots_canonical ON poll_endpoint_snapshots(canonical_id);

CREATE TABLE IF NOT EXISTS poll_provider_refs (
    canonical_id TEXT NOT NULL REFERENCES canonical_messages(canonical_id) ON DELETE CASCADE,
    endpoint_id TEXT NOT NULL,
    provider_kind TEXT NOT NULL,
    provider_ref TEXT NOT NULL,
    PRIMARY KEY (canonical_id, endpoint_id, provider_kind),
    UNIQUE (endpoint_id, provider_kind, provider_ref)
);

CREATE TABLE IF NOT EXISTS poll_result_companions (
    canonical_id TEXT NOT NULL REFERENCES canonical_messages(canonical_id) ON DELETE CASCADE,
    endpoint_id TEXT NOT NULL,
    remote_message_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (canonical_id, endpoint_id),
    UNIQUE (endpoint_id, remote_message_id)
);

CREATE TABLE IF NOT EXISTS suppressed_reactions (
    endpoint_id TEXT NOT NULL,
    remote_message_id TEXT NOT NULL,
    emoji TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (endpoint_id, remote_message_id, emoji)
);

CREATE TABLE IF NOT EXISTS suppressed_local_messages (
    endpoint_id TEXT NOT NULL,
    remote_message_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (endpoint_id, remote_message_id)
);

CREATE TABLE IF NOT EXISTS delivery_operations (
    canonical_id TEXT NOT NULL REFERENCES canonical_messages(canonical_id) ON DELETE CASCADE,
    endpoint_id TEXT NOT NULL,
    operation_kind TEXT NOT NULL CHECK (length(operation_kind) > 0),
    operation_revision INTEGER NOT NULL CHECK (operation_revision >= 0),
    state TEXT NOT NULL CHECK (state IN ('queued', 'retrying', 'awaiting_replay', 'failed')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at INTEGER,
    failure_class TEXT CHECK (failure_class IS NULL OR failure_class IN ('transient', 'rate_limited', 'permission_denied', 'destination_missing', 'payload_rejected', 'unsupported')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (canonical_id, endpoint_id, operation_kind, operation_revision)
);
CREATE INDEX IF NOT EXISTS idx_delivery_operations_endpoint_state ON delivery_operations(endpoint_id, state);
CREATE INDEX IF NOT EXISTS idx_delivery_operations_active_age ON delivery_operations(state, updated_at);

-- Create sub-steps contain no payload. They prevent a successful compatibility
-- companion or provider create from being repeated while its copy is persisted.
CREATE TABLE IF NOT EXISTS delivery_create_steps (
    canonical_id TEXT NOT NULL REFERENCES canonical_messages(canonical_id) ON DELETE CASCADE,
    endpoint_id TEXT NOT NULL,
    operation_revision INTEGER NOT NULL CHECK (operation_revision >= 0),
    step_kind TEXT NOT NULL CHECK (step_kind IN ('companion', 'primary')),
    state TEXT NOT NULL CHECK (state IN ('pending', 'complete', 'ambiguous')),
    remote_message_id TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (canonical_id, endpoint_id, operation_revision, step_kind)
);
CREATE INDEX IF NOT EXISTS idx_delivery_create_steps_state ON delivery_create_steps(state, updated_at);
