PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL COLLATE NOCASE UNIQUE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('admin', 'operator')),
    active BOOLEAN NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    revoked_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

CREATE TABLE IF NOT EXISTS user_invites (
    id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    target_role TEXT NOT NULL CHECK (target_role IN ('admin', 'operator')),
    expires_at INTEGER NOT NULL,
    creator_user_id TEXT NOT NULL REFERENCES users(id),
    consumed_at INTEGER
);

CREATE TABLE IF NOT EXISTS password_reset_tokens (
    token_hash TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    consumed_at INTEGER
);

CREATE TABLE IF NOT EXISTS audit_events (
    id TEXT PRIMARY KEY,
    actor_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    action TEXT NOT NULL CHECK (length(action) > 0),
    target_id TEXT,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_events_created ON audit_events(created_at);

CREATE TABLE IF NOT EXISTS verification_pipelines (
    id TEXT PRIMARY KEY,
    public_token TEXT NOT NULL UNIQUE,
    label TEXT NOT NULL CHECK (length(label) > 0),
    target_transport TEXT NOT NULL CHECK (target_transport IN ('whatsapp', 'discord')),
    endpoint_alias TEXT NOT NULL,
    discord_role_id TEXT,
    enabled BOOLEAN NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    creator_user_id TEXT NOT NULL REFERENCES users(id),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS membership_requests (
    id TEXT PRIMARY KEY,
    pipeline_id TEXT NOT NULL REFERENCES verification_pipelines(id),
    status TEXT NOT NULL CHECK (status IN ('pending_email', 'pending_admin', 'pending', 'approved', 'rejected', 'fulfilled', 'cancelled', 'expired')),
    applicant_work_email TEXT NOT NULL,
    applicant_whatsapp_phone TEXT,
    applicant_discord_user_id TEXT,
    linkedin_url TEXT,
    evidence_reference TEXT,
    evidence_metadata TEXT,
    verification_state TEXT NOT NULL CHECK (verification_state IN ('pending', 'in_progress', 'verified', 'failed', 'unavailable')),
    fulfillment_state TEXT NOT NULL DEFAULT 'not_started' CHECK (fulfillment_state IN ('not_started', 'succeeded', 'action_pending', 'failed')),
    fulfillment_failure_class TEXT,
    decided_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    decision_reason TEXT,
    decided_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_membership_requests_pipeline_status ON membership_requests(pipeline_id, status);

CREATE TABLE IF NOT EXISTS email_challenges (
    id TEXT PRIMARY KEY,
    membership_request_id TEXT NOT NULL REFERENCES membership_requests(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at INTEGER NOT NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    verified_at INTEGER
);

CREATE TABLE IF NOT EXISTS verification_assessments (
    id TEXT PRIMARY KEY,
    membership_request_id TEXT NOT NULL REFERENCES membership_requests(id) ON DELETE CASCADE,
    assessment_kind TEXT NOT NULL CHECK (assessment_kind IN ('work_email', 'deterministic', 'openrouter')),
    state TEXT NOT NULL CHECK (state IN ('pending', 'complete', 'failed', 'unavailable')),
    result_code TEXT,
    confidence REAL CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    detail TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (membership_request_id, assessment_kind)
);
