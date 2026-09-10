from pathlib import Path


def replace(path, old, new, count=1):
    p = Path(path)
    text = p.read_text()
    actual = text.count(old)
    if actual < count:
        raise SystemExit(f"{path}: expected at least {count} occurrence(s), found {actual}: {old[:100]!r}")
    text = text.replace(old, new, count)
    p.write_text(text)


def replace_all(path, old, new, expected=None):
    p = Path(path)
    text = p.read_text()
    actual = text.count(old)
    if expected is not None and actual != expected:
        raise SystemExit(f"{path}: expected {expected} occurrence(s), found {actual}: {old[:100]!r}")
    if actual == 0:
        raise SystemExit(f"{path}: no occurrence: {old[:100]!r}")
    p.write_text(text.replace(old, new))


def create(path, content):
    p = Path(path)
    if p.exists():
        raise SystemExit(f"{path}: already exists")
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(content)


# control-store schema and restart-safe migration for existing rows.
replace(
    "internal/controlstore/schema.sql",
    "    enabled BOOLEAN NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),\n    encrypted_credential BLOB,",
    "    enabled BOOLEAN NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),\n    integration_mode TEXT NOT NULL DEFAULT '',\n    encrypted_credential BLOB,",
)

replace(
    "internal/controlstore/controlstore.go",
    "func Open(ctx context.Context, path string) (*Store, error) {",
    '''func ensureTransportConnectionIntegrationMode(ctx context.Context, db *sql.DB) error {
\trows, err := db.QueryContext(ctx, `PRAGMA table_info(transport_connections)`)
\tif err != nil {
\t\treturn fmt.Errorf("inspect transport connection schema: %w", err)
\t}
\tfound := false
\tfor rows.Next() {
\t\tvar cid, notNull, primaryKey int
\t\tvar name, columnType string
\t\tvar defaultValue any
\t\tif err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
\t\t\trows.Close()
\t\t\treturn fmt.Errorf("scan transport connection schema: %w", err)
\t\t}
\t\tif name == "integration_mode" {
\t\t\tfound = true
\t\t}
\t}
\tif err := rows.Err(); err != nil {
\t\trows.Close()
\t\treturn fmt.Errorf("iterate transport connection schema: %w", err)
\t}
\trows.Close()
\tif !found {
\t\tif _, err := db.ExecContext(ctx, `ALTER TABLE transport_connections ADD COLUMN integration_mode TEXT NOT NULL DEFAULT ''`); err != nil {
\t\t\treturn fmt.Errorf("add transport connection integration mode: %w", err)
\t\t}
\t}
\tif _, err := db.ExecContext(ctx, `UPDATE transport_connections SET integration_mode='bot' WHERE transport='telegram' AND TRIM(integration_mode)=''`); err != nil {
\t\treturn fmt.Errorf("default existing Telegram integration modes: %w", err)
\t}
\treturn nil
}

func Open(ctx context.Context, path string) (*Store, error) {''',
)

replace(
    "internal/controlstore/controlstore.go",
    '''\tif err := tx.Commit(); err != nil {
\t\t_ = db.Close()
\t\treturn nil, fmt.Errorf("commit control schema initialization: %w", err)
\t}
\tif path != ":memory:" {''',
    '''\tif err := tx.Commit(); err != nil {
\t\t_ = db.Close()
\t\treturn nil, fmt.Errorf("commit control schema initialization: %w", err)
\t}
\tif err := ensureTransportConnectionIntegrationMode(ctx, db); err != nil {
\t\t_ = db.Close()
\t\treturn nil, err
\t}
\tif path != ":memory:" {''',
)

# Connection metadata, validation, and persistence.
replace(
    "internal/controlstore/connection.go",
    'var connectionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)\n',
    '''var connectionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

const (
\tTelegramIntegrationModeBot     = "bot"
\tTelegramIntegrationModeMTProto = "mtproto"
)

func NormalizeIntegrationMode(transportName, mode string) string {
\ttransportName = strings.ToLower(strings.TrimSpace(transportName))
\tmode = strings.ToLower(strings.TrimSpace(mode))
\tif transportName == "telegram" && mode == "" {
\t\treturn TelegramIntegrationModeBot
\t}
\tif transportName != "telegram" {
\t\treturn ""
\t}
\treturn mode
}

func ValidateIntegrationMode(transportName, mode string) error {
\ttransportName = strings.ToLower(strings.TrimSpace(transportName))
\tif transportName != "telegram" {
\t\tif strings.TrimSpace(mode) != "" {
\t\t\treturn fmt.Errorf("integration mode is only supported for telegram connections")
\t\t}
\t\treturn nil
\t}
\tswitch NormalizeIntegrationMode(transportName, mode) {
\tcase TelegramIntegrationModeBot, TelegramIntegrationModeMTProto:
\t\treturn nil
\tdefault:
\t\treturn fmt.Errorf("invalid telegram integration mode %q: must be bot or mtproto", strings.TrimSpace(mode))
\t}
}
''',
)

replace(
    "internal/controlstore/connection.go",
    '''\tID                   string  `json:"id"`
\tTransport            string  `json:"transport"`
\tLabel                string  `json:"label"`''',
    '''\tID                   string  `json:"id"`
\tTransport            string  `json:"transport"`
\tIntegrationMode      string  `json:"integrationMode,omitempty"`
\tLabel                string  `json:"label"`''',
)

replace(
    "internal/controlstore/connection.go",
    '''\tswitch c.Transport {
\tcase "whatsapp":
\t\tif len(c.EncryptedCredential) > 0 || len(c.CredentialNonce) > 0 {
\t\t\treturn errors.New("whatsapp connections must not store credentials")
\t\t}
\tcase "discord", "telegram":
\t\tif len(c.EncryptedCredential) == 0 || len(c.CredentialNonce) == 0 {
\t\t\treturn fmt.Errorf("%s connections require encrypted credentials", c.Transport)
\t\t}
\tdefault:
\t\treturn fmt.Errorf("invalid transport %q: must be whatsapp, discord, or telegram", c.Transport)
\t}''',
    '''\tif err := ValidateIntegrationMode(c.Transport, c.IntegrationMode); err != nil {
\t\treturn err
\t}
\tswitch c.Transport {
\tcase "whatsapp":
\t\tif len(c.EncryptedCredential) > 0 || len(c.CredentialNonce) > 0 {
\t\t\treturn errors.New("whatsapp connections must not store credentials")
\t\t}
\tcase "discord", "telegram":
\t\tif len(c.EncryptedCredential) == 0 || len(c.CredentialNonce) == 0 {
\t\t\treturn fmt.Errorf("%s connections require encrypted credentials", c.Transport)
\t\t}
\tdefault:
\t\treturn fmt.Errorf("invalid transport %q: must be whatsapp, discord, or telegram", c.Transport)
\t}''',
)

replace(
    "internal/controlstore/connection.go",
    '''\tif conn.CredentialKeyVersion == 0 {
\t\tconn.CredentialKeyVersion = CurrentKeyVersion
\t}
\tif err := conn.Validate(); err != nil {''',
    '''\tif conn.CredentialKeyVersion == 0 {
\t\tconn.CredentialKeyVersion = CurrentKeyVersion
\t}
\tif err := ValidateIntegrationMode(conn.Transport, conn.IntegrationMode); err != nil {
\t\treturn err
\t}
\tconn.IntegrationMode = NormalizeIntegrationMode(conn.Transport, conn.IntegrationMode)
\tif err := conn.Validate(); err != nil {''',
)

replace(
    "internal/controlstore/connection.go",
    '''INSERT INTO transport_connections (id, transport, label, enabled, encrypted_credential, credential_nonce, credential_key_version, created_by, created_at, updated_at)
\t\tVALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)''',
    '''INSERT INTO transport_connections (id, transport, integration_mode, label, enabled, encrypted_credential, credential_nonce, credential_key_version, created_by, created_at, updated_at)
\t\tVALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)''',
)
replace(
    "internal/controlstore/connection.go",
    ''', conn.ID, conn.Transport, conn.Label, conn.Enabled, encCred, nonce, conn.CredentialKeyVersion, conn.CreatedBy, conn.CreatedAt, conn.UpdatedAt)''',
    ''', conn.ID, conn.Transport, conn.IntegrationMode, conn.Label, conn.Enabled, encCred, nonce, conn.CredentialKeyVersion, conn.CreatedBy, conn.CreatedAt, conn.UpdatedAt)''',
)

replace_all(
    "internal/controlstore/connection.go",
    "SELECT id, transport, label, enabled, encrypted_credential, credential_nonce, credential_key_version, created_by, created_at, updated_at",
    "SELECT id, transport, integration_mode, label, enabled, encrypted_credential, credential_nonce, credential_key_version, created_by, created_at, updated_at",
    expected=2,
)
replace_all(
    "internal/controlstore/connection.go",
    "row.Scan(&conn.ID, &conn.Transport, &conn.Label, &conn.Enabled, &encCred, &nonce, &conn.CredentialKeyVersion, &createdBy, &conn.CreatedAt, &conn.UpdatedAt)",
    "row.Scan(&conn.ID, &conn.Transport, &conn.IntegrationMode, &conn.Label, &conn.Enabled, &encCred, &nonce, &conn.CredentialKeyVersion, &createdBy, &conn.CreatedAt, &conn.UpdatedAt)",
    expected=1,
)
replace(
    "internal/controlstore/connection.go",
    '''\tconn.EncryptedCredential = encCred
\tconn.CredentialNonce = nonce
\tif createdBy.Valid {''',
    '''\tconn.EncryptedCredential = encCred
\tconn.CredentialNonce = nonce
\tconn.IntegrationMode = NormalizeIntegrationMode(conn.Transport, conn.IntegrationMode)
\tif createdBy.Valid {''',
)
replace(
    "internal/controlstore/connection.go",
    "rows.Scan(&conn.ID, &conn.Transport, &conn.Label, &conn.Enabled, &encCred, &nonce, &conn.CredentialKeyVersion, &createdBy, &conn.CreatedAt, &conn.UpdatedAt)",
    "rows.Scan(&conn.ID, &conn.Transport, &conn.IntegrationMode, &conn.Label, &conn.Enabled, &encCred, &nonce, &conn.CredentialKeyVersion, &createdBy, &conn.CreatedAt, &conn.UpdatedAt)",
)
replace(
    "internal/controlstore/connection.go",
    '''\t\tconn.EncryptedCredential = encCred
\t\tconn.CredentialNonce = nonce
\t\tif createdBy.Valid {''',
    '''\t\tconn.EncryptedCredential = encCred
\t\tconn.CredentialNonce = nonce
\t\tconn.IntegrationMode = NormalizeIntegrationMode(conn.Transport, conn.IntegrationMode)
\t\tif createdBy.Valid {''',
)
replace(
    "internal/controlstore/connection.go",
    '''\tif err := conn.Validate(); err != nil {
\t\treturn err
\t}
\tnow := time.Now().UnixMilli()
\tconn.UpdatedAt = now''',
    '''\tif err := ValidateIntegrationMode(conn.Transport, conn.IntegrationMode); err != nil {
\t\treturn err
\t}
\tconn.IntegrationMode = NormalizeIntegrationMode(conn.Transport, conn.IntegrationMode)
\tif err := conn.Validate(); err != nil {
\t\treturn err
\t}
\tnow := time.Now().UnixMilli()
\tconn.UpdatedAt = now''',
)
replace(
    "internal/controlstore/connection.go",
    '''SET label = ?, enabled = ?, encrypted_credential = ?, credential_nonce = ?, credential_key_version = ?, updated_at = ?
\t\tWHERE id = ?
\t`, conn.Label, conn.Enabled, encCred, nonce, conn.CredentialKeyVersion, conn.UpdatedAt, conn.ID)''',
    '''SET integration_mode = ?, label = ?, enabled = ?, encrypted_credential = ?, credential_nonce = ?, credential_key_version = ?, updated_at = ?
\t\tWHERE id = ?
\t`, conn.IntegrationMode, conn.Label, conn.Enabled, encCred, nonce, conn.CredentialKeyVersion, conn.UpdatedAt, conn.ID)''',
)

# Capability descriptor stays admin/presentation metadata, not routing identity.
create(
    "internal/transport/telegram/capabilities.go",
    '''package telegram

import (
\t"errors"
\t"strings"
)

type DiscoveryMode string

const (
\tDiscoveryObserved DiscoveryMode = "observed"
\tDiscoveryFull     DiscoveryMode = "full"
)

type Capabilities struct {
\tChatDiscovery     DiscoveryMode `json:"chatDiscovery"`
\tTopicDiscovery    DiscoveryMode `json:"topicDiscovery"`
\tHistoryRecovery   bool          `json:"historyRecovery"`
\tPrivacyModeStatus bool          `json:"privacyModeStatus"`
\tPolls             bool          `json:"polls"`
}

var ErrMTProtoAdapterUnavailable = errors.New("Telegram MTProto connection requires authentication before the transport can start")

func CapabilitiesForIntegrationMode(mode string) Capabilities {
\tif strings.EqualFold(strings.TrimSpace(mode), "mtproto") {
\t\treturn Capabilities{
\t\t\tChatDiscovery:     DiscoveryFull,
\t\t\tTopicDiscovery:    DiscoveryFull,
\t\t\tHistoryRecovery:   true,
\t\t\tPrivacyModeStatus: false,
\t\t\tPolls:             false,
\t\t}
\t}
\treturn Capabilities{
\t\tChatDiscovery:     DiscoveryObserved,
\t\tTopicDiscovery:    DiscoveryObserved,
\t\tHistoryRecovery:   false,
\t\tPrivacyModeStatus: true,
\t\tPolls:             true,
\t}
}
''',
)
replace(
    "internal/transport/telegram/admin.go",
    '''\tVisibilityGuidance string              `json:"visibilityGuidance"`
}''',
    '''\tVisibilityGuidance string              `json:"visibilityGuidance"`
\tCapabilities       Capabilities          `json:"capabilities"`
}''',
)
replace(
    "internal/transport/telegram/admin.go",
    '''\t\tPrivacyModeKnown:   false,
\t\tVisibilityGuidance: VisibilityGuidance,
\t}''',
    '''\t\tPrivacyModeKnown:   false,
\t\tVisibilityGuidance: VisibilityGuidance,
\t\tCapabilities:       CapabilitiesForIntegrationMode("bot"),
\t}''',
)

# API DTO helpers and integration-mode validation.
replace(
    "internal/api/connections.go",
    '"github.com/vm75/message-sync/internal/safelog"\n)',
    '"github.com/vm75/message-sync/internal/safelog"\n\ttelegram "github.com/vm75/message-sync/internal/transport/telegram"\n)',
)
replace(
    "internal/api/connections.go",
    '''\tID        string `json:"id"`
\tTransport string `json:"transport"`
\tLabel     string `json:"label"`''',
    '''\tID              string                 `json:"id"`
\tTransport       string                 `json:"transport"`
\tIntegrationMode string                 `json:"integrationMode,omitempty"`
\tCapabilities    *telegram.Capabilities `json:"capabilities,omitempty"`
\tLabel           string                 `json:"label"`''',
)
replace(
    "internal/api/connections.go",
    '''\tEnabled   *bool  `json:"enabled,omitempty"`
\tToken     string `json:"token,omitempty"`''',
    '''\tEnabled         *bool  `json:"enabled,omitempty"`
\tToken           string `json:"token,omitempty"`
\tIntegrationMode string `json:"integrationMode,omitempty"`''',
)

replace_all(
    "internal/api/connections.go",
    "SELECT id, transport, label, enabled, created_at, updated_at",
    "SELECT id, transport, integration_mode, label, enabled, created_at, updated_at",
    expected=4,
)
replace_all(
    "internal/api/connections.go",
    "&dto.ID, &dto.Transport, &dto.Label, &dto.Enabled, &dto.CreatedAt, &dto.UpdatedAt",
    "&dto.ID, &dto.Transport, &dto.IntegrationMode, &dto.Label, &dto.Enabled, &dto.CreatedAt, &dto.UpdatedAt",
    expected=2,
)
replace_all(
    "internal/api/connections.go",
    "&conn.ID, &conn.Transport, &conn.Label, &conn.Enabled, &conn.CreatedAt, &conn.UpdatedAt",
    "&conn.ID, &conn.Transport, &conn.IntegrationMode, &conn.Label, &conn.Enabled, &conn.CreatedAt, &conn.UpdatedAt",
    expected=2,
)
replace(
    "internal/api/connections.go",
    '''\t\tconns = append(conns, dto)
\t}''',
    '''\t\tapplyConnectionCapabilities(&dto)
\t\tconns = append(conns, dto)
\t}''',
)
replace(
    "internal/api/connections.go",
    '''\tif err != nil {
\t\tsafelog.Error(s.logger, "query connection failed", "connection_get", err)
\t\tWriteError(w, http.StatusInternalServerError, "failed to query connection")
\t\treturn
\t}
\t_ = WriteJSON(w, http.StatusOK, dto)''',
    '''\tif err != nil {
\t\tsafelog.Error(s.logger, "query connection failed", "connection_get", err)
\t\tWriteError(w, http.StatusInternalServerError, "failed to query connection")
\t\treturn
\t}
\tapplyConnectionCapabilities(&dto)
\t_ = WriteJSON(w, http.StatusOK, dto)''',
)

replace(
    "internal/api/connections.go",
    '''\treq.ID = strings.TrimSpace(req.ID)
\treq.Token = strings.TrimSpace(req.Token)

\tif req.Transport != "whatsapp"''',
    '''\treq.ID = strings.TrimSpace(req.ID)
\treq.Token = strings.TrimSpace(req.Token)
\treq.IntegrationMode = strings.TrimSpace(req.IntegrationMode)

\tif req.Transport != "whatsapp"''',
)
replace(
    "internal/api/connections.go",
    '''\tif req.Label == "" {
\t\tWriteError(w, http.StatusBadRequest, "connection label is required")
\t\treturn
\t}
''',
    '''\tif err := controlstore.ValidateIntegrationMode(req.Transport, req.IntegrationMode); err != nil {
\t\tWriteError(w, http.StatusBadRequest, err.Error())
\t\treturn
\t}
\treq.IntegrationMode = controlstore.NormalizeIntegrationMode(req.Transport, req.IntegrationMode)
\tif req.Label == "" {
\t\tWriteError(w, http.StatusBadRequest, "connection label is required")
\t\treturn
\t}
''',
)

replace(
    "internal/api/connections.go",
    '''\tvar encCred, nonce []byte
\tif req.Transport == "whatsapp" {
\t\tif req.Token != "" {
\t\t\tWriteError(w, http.StatusBadRequest, "whatsapp connections must not store credentials")
\t\t\treturn
\t\t}
\t} else {
\t\tif req.Token == "" {
\t\t\tWriteError(w, http.StatusBadRequest, req.Transport+" connections require a bot token")
\t\t\treturn
\t\t}
\t\tif s.credentialCipher == nil {
\t\t\tWriteError(w, http.StatusInternalServerError, "credential cipher unavailable")
\t\t\treturn
\t\t}
\t\tvar encErr error
\t\tencCred, nonce, encErr = s.credentialCipher.Encrypt([]byte(req.Token))
\t\tif encErr != nil {
\t\t\tsafelog.Error(s.logger, "encrypt connection credential failed", "connection_create", encErr)
\t\t\tWriteError(w, http.StatusInternalServerError, "failed to secure credentials")
\t\t\treturn
\t\t}
\t}''',
    '''\tvar credential []byte
\tswitch {
\tcase req.Transport == "whatsapp":
\t\tif req.Token != "" {
\t\t\tWriteError(w, http.StatusBadRequest, "whatsapp connections must not store credentials")
\t\t\treturn
\t\t}
\tcase req.Transport == "discord":
\t\tif req.Token == "" {
\t\t\tWriteError(w, http.StatusBadRequest, "discord connections require a bot token")
\t\t\treturn
\t\t}
\t\tcredential = []byte(req.Token)
\tcase req.Transport == "telegram" && req.IntegrationMode == controlstore.TelegramIntegrationModeBot:
\t\tif req.Token == "" {
\t\t\tWriteError(w, http.StatusBadRequest, "telegram connections require a bot token")
\t\t\treturn
\t\t}
\t\tcredential = []byte(req.Token)
\tcase req.Transport == "telegram" && req.IntegrationMode == controlstore.TelegramIntegrationModeMTProto:
\t\tif req.Token != "" {
\t\t\tWriteError(w, http.StatusBadRequest, "mtproto connections do not accept bot tokens")
\t\t\treturn
\t\t}
\t\tcredential = []byte(`{"version":1}`)
\t}

\tvar encCred, nonce []byte
\tif len(credential) > 0 {
\t\tif s.credentialCipher == nil {
\t\t\tWriteError(w, http.StatusInternalServerError, "credential cipher unavailable")
\t\t\treturn
\t\t}
\t\tvar encErr error
\t\tencCred, nonce, encErr = s.credentialCipher.Encrypt(credential)
\t\tif encErr != nil {
\t\t\tsafelog.Error(s.logger, "encrypt connection credential failed", "connection_create", encErr)
\t\t\tWriteError(w, http.StatusInternalServerError, "failed to secure credentials")
\t\t\treturn
\t\t}
\t}''',
)
replace(
    "internal/api/connections.go",
    '''\t\tID:                   req.ID,
\t\tTransport:            req.Transport,
\t\tLabel:                req.Label,''',
    '''\t\tID:                   req.ID,
\t\tTransport:            req.Transport,
\t\tIntegrationMode:      req.IntegrationMode,
\t\tLabel:                req.Label,''',
)
replace(
    "internal/api/connections.go",
    '''INSERT INTO transport_connections (id, transport, label, enabled, encrypted_credential, credential_nonce, credential_key_version, created_by, created_at, updated_at)
\t\t\tVALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
\t\t`, conn.ID, conn.Transport, conn.Label, conn.Enabled, encVal, nonceVal, conn.CredentialKeyVersion, conn.CreatedBy, conn.CreatedAt, conn.UpdatedAt)''',
    '''INSERT INTO transport_connections (id, transport, integration_mode, label, enabled, encrypted_credential, credential_nonce, credential_key_version, created_by, created_at, updated_at)
\t\t\tVALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
\t\t`, conn.ID, conn.Transport, conn.IntegrationMode, conn.Label, conn.Enabled, encVal, nonceVal, conn.CredentialKeyVersion, conn.CreatedBy, conn.CreatedAt, conn.UpdatedAt)''',
)
replace(
    "internal/api/connections.go",
    '''\t_ = WriteJSON(w, http.StatusCreated, ConnectionDTO{
\t\tID:        conn.ID,
\t\tTransport: conn.Transport,
\t\tLabel:     conn.Label,
\t\tEnabled:   conn.Enabled,
\t\tCreatedAt: conn.CreatedAt,
\t\tUpdatedAt: conn.UpdatedAt,
\t})''',
    '''\t_ = WriteJSON(w, http.StatusCreated, connectionDTOFromControl(conn))''',
)

replace_all(
    "internal/api/connections.go",
    "SELECT id, transport, label, enabled, encrypted_credential, credential_nonce, credential_key_version, created_by, created_at, updated_at",
    "SELECT id, transport, integration_mode, label, enabled, encrypted_credential, credential_nonce, credential_key_version, created_by, created_at, updated_at",
    expected=1,
)
replace(
    "internal/api/connections.go",
    "&conn.ID, &conn.Transport, &conn.Label, &conn.Enabled, &encCred, &nonce, &conn.CredentialKeyVersion, &createdBy, &conn.CreatedAt, &conn.UpdatedAt",
    "&conn.ID, &conn.Transport, &conn.IntegrationMode, &conn.Label, &conn.Enabled, &encCred, &nonce, &conn.CredentialKeyVersion, &createdBy, &conn.CreatedAt, &conn.UpdatedAt",
)
replace(
    "internal/api/connections.go",
    '''\tconn.EncryptedCredential = encCred
\tconn.CredentialNonce = nonce
\toldEncCred := append([]byte(nil), encCred...)''',
    '''\tconn.EncryptedCredential = encCred
\tconn.CredentialNonce = nonce
\tconn.IntegrationMode = controlstore.NormalizeIntegrationMode(conn.Transport, conn.IntegrationMode)
\toldEncCred := append([]byte(nil), encCred...)''',
)
replace(
    "internal/api/connections.go",
    '''\tcredentialReplaced := req.Token != nil
\tif credentialReplaced {
\t\tif conn.Transport == "whatsapp" {''',
    '''\tcredentialReplaced := req.Token != nil
\tif credentialReplaced {
\t\tif conn.Transport == "telegram" && conn.IntegrationMode == controlstore.TelegramIntegrationModeMTProto {
\t\t\tWriteError(w, http.StatusBadRequest, "mtproto credentials are managed through Telegram authentication endpoints")
\t\t\treturn
\t\t}
\t\tif conn.Transport == "whatsapp" {''',
)
replace(
    "internal/api/connections.go",
    '''\t_ = WriteJSON(w, http.StatusOK, ConnectionDTO{
\t\tID:        conn.ID,
\t\tTransport: conn.Transport,
\t\tLabel:     conn.Label,
\t\tEnabled:   conn.Enabled,
\t\tCreatedAt: conn.CreatedAt,
\t\tUpdatedAt: conn.UpdatedAt,
\t})''',
    '''\t_ = WriteJSON(w, http.StatusOK, connectionDTOFromControl(conn))''',
)

# Apply capabilities to status/discovery DTOs after database reads.
replace_all(
    "internal/api/connections.go",
    '''\tif !conn.Enabled {
''',
    '''\tapplyConnectionCapabilities(&conn)
\tif !conn.Enabled {
''',
    expected=2,
)

create(
    "internal/api/connection_capabilities.go",
    '''package api

import (
\t"github.com/vm75/message-sync/internal/controlstore"
\ttelegram "github.com/vm75/message-sync/internal/transport/telegram"
)

func applyConnectionCapabilities(dto *ConnectionDTO) {
\tif dto == nil {
\t\treturn
\t}
\tdto.IntegrationMode = controlstore.NormalizeIntegrationMode(dto.Transport, dto.IntegrationMode)
\tif dto.Transport != "telegram" {
\t\tdto.IntegrationMode = ""
\t\tdto.Capabilities = nil
\t\treturn
\t}
\tcaps := telegram.CapabilitiesForIntegrationMode(dto.IntegrationMode)
\tdto.Capabilities = &caps
}

func connectionDTOFromControl(conn controlstore.Connection) ConnectionDTO {
\tdto := ConnectionDTO{
\t\tID:              conn.ID,
\t\tTransport:       conn.Transport,
\t\tIntegrationMode: conn.IntegrationMode,
\t\tLabel:           conn.Label,
\t\tEnabled:         conn.Enabled,
\t\tCreatedAt:       conn.CreatedAt,
\t\tUpdatedAt:       conn.UpdatedAt,
\t}
\tapplyConnectionCapabilities(&dto)
\treturn dto
}
''',
)

# Select the adapter by connection metadata; the MTProto opener is implemented in #103.
replace(
    "internal/app/app.go",
    '''var openTelegram = func(ctx context.Context, opts telegram.Options) (telegramTransport, error) {
\treturn telegram.Open(ctx, opts)
}
''',
    '''var openTelegram = func(ctx context.Context, opts telegram.Options) (telegramTransport, error) {
\treturn telegram.Open(ctx, opts)
}

var openTelegramMTProto = func(context.Context, telegram.Options) (telegramTransport, error) {
\treturn nil, telegram.ErrMTProtoAdapterUnavailable
}

func openTelegramForIntegrationMode(ctx context.Context, mode string, opts telegram.Options) (telegramTransport, error) {
\tif controlstore.NormalizeIntegrationMode("telegram", mode) == controlstore.TelegramIntegrationModeMTProto {
\t\treturn openTelegramMTProto(ctx, opts)
\t}
\treturn openTelegram(ctx, opts)
}
''',
)
replace_all(
    "internal/app/app.go",
    "tgInst, err := openTelegram(ctx, telegram.Options{",
    "tgInst, err := openTelegramForIntegrationMode(ctx, c.IntegrationMode, telegram.Options{",
    expected=2,
)

# Focused tests for schema migration, API behavior/capabilities, and adapter selection.
create(
    "internal/controlstore/connection_mode_test.go",
    '''package controlstore

import (
\t"context"
\t"database/sql"
\t"path/filepath"
\t"testing"

\t_ "modernc.org/sqlite"
)

func TestTransportConnectionIntegrationModeMigrationDefaultsTelegramToBot(t *testing.T) {
\tpath := filepath.Join(t.TempDir(), "control.db")
\tdb, err := sql.Open("sqlite", path)
\tif err != nil {
\t\tt.Fatal(err)
\t}
\t_, err = db.Exec(`CREATE TABLE transport_connections (
\t\tid TEXT PRIMARY KEY,
\t\ttransport TEXT NOT NULL,
\t\tlabel TEXT NOT NULL,
\t\tenabled BOOLEAN NOT NULL DEFAULT 1,
\t\tencrypted_credential BLOB,
\t\tcredential_nonce BLOB,
\t\tcredential_key_version INTEGER NOT NULL DEFAULT 1,
\t\tcreated_by TEXT,
\t\tcreated_at INTEGER NOT NULL,
\t\tupdated_at INTEGER NOT NULL
\t)`)
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tif _, err := db.Exec(`INSERT INTO transport_connections(id,transport,label,enabled,encrypted_credential,credential_nonce,created_at,updated_at) VALUES('legacy','telegram','Legacy bot',1,x'01',x'02',1,1)`); err != nil {
\t\tt.Fatal(err)
\t}
\t_ = db.Close()

\tstore, err := Open(context.Background(), path)
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tdefer store.Close()
\tconn, err := store.GetConnection(context.Background(), "legacy")
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tif conn.IntegrationMode != TelegramIntegrationModeBot {
\t\tt.Fatalf("legacy Telegram mode = %q, want bot", conn.IntegrationMode)
\t}
}

func TestValidateIntegrationMode(t *testing.T) {
\tfor _, tc := range []struct {
\t\ttransport string
\t\tmode      string
\t\twantErr   bool
\t}{
\t\t{"telegram", "", false},
\t\t{"telegram", "bot", false},
\t\t{"telegram", "mtproto", false},
\t\t{"telegram", "other", true},
\t\t{"discord", "", false},
\t\t{"discord", "bot", true},
\t} {
\t\tif err := ValidateIntegrationMode(tc.transport, tc.mode); (err != nil) != tc.wantErr {
\t\t\tt.Errorf("ValidateIntegrationMode(%q,%q) err=%v wantErr=%v", tc.transport, tc.mode, err, tc.wantErr)
\t\t}
\t}
}
''',
)

create(
    "internal/transport/telegram/capabilities_test.go",
    '''package telegram

import "testing"

func TestCapabilitiesForIntegrationMode(t *testing.T) {
\tbot := CapabilitiesForIntegrationMode("bot")
\tif bot.ChatDiscovery != DiscoveryObserved || bot.TopicDiscovery != DiscoveryObserved || bot.HistoryRecovery || !bot.PrivacyModeStatus || !bot.Polls {
\t\tt.Fatalf("unexpected bot capabilities: %+v", bot)
\t}
\tmt := CapabilitiesForIntegrationMode("mtproto")
\tif mt.ChatDiscovery != DiscoveryFull || mt.TopicDiscovery != DiscoveryFull || !mt.HistoryRecovery || mt.PrivacyModeStatus || mt.Polls {
\t\tt.Fatalf("unexpected mtproto foundation capabilities: %+v", mt)
\t}
}
''',
)

create(
    "internal/app/telegram_mode_test.go",
    '''package app

import (
\t"context"
\t"errors"
\t"testing"

\t"github.com/vm75/message-sync/internal/controlstore"
\ttelegram "github.com/vm75/message-sync/internal/transport/telegram"
)

func TestOpenTelegramForIntegrationModeSelectsExactlyOneOpener(t *testing.T) {
\toriginalBot := openTelegram
\toriginalMTProto := openTelegramMTProto
\tt.Cleanup(func() {
\t\topenTelegram = originalBot
\t\topenTelegramMTProto = originalMTProto
\t})
\tbotErr := errors.New("bot selected")
\tmtprotoErr := errors.New("mtproto selected")
\topenTelegram = func(context.Context, telegram.Options) (telegramTransport, error) { return nil, botErr }
\topenTelegramMTProto = func(context.Context, telegram.Options) (telegramTransport, error) { return nil, mtprotoErr }

\tif _, err := openTelegramForIntegrationMode(context.Background(), "", telegram.Options{}); !errors.Is(err, botErr) {
\t\tt.Fatalf("legacy/empty mode selected %v, want bot opener", err)
\t}
\tif _, err := openTelegramForIntegrationMode(context.Background(), controlstore.TelegramIntegrationModeBot, telegram.Options{}); !errors.Is(err, botErr) {
\t\tt.Fatalf("bot mode selected %v, want bot opener", err)
\t}
\tif _, err := openTelegramForIntegrationMode(context.Background(), controlstore.TelegramIntegrationModeMTProto, telegram.Options{}); !errors.Is(err, mtprotoErr) {
\t\tt.Fatalf("mtproto mode selected %v, want MTProto opener", err)
\t}
}
''',
)

create(
    "internal/api/telegram_mode_test.go",
    '''package api

import (
\t"encoding/json"
\t"net/http"
\t"net/http/httptest"
\t"strings"
\t"testing"

\t"github.com/vm75/message-sync/internal/controlstore"
\ttelegram "github.com/vm75/message-sync/internal/transport/telegram"
)

func authenticatedConnectionRequest(t *testing.T, srv *Server, token, method, path, body string) *httptest.ResponseRecorder {
\tt.Helper()
\treq := httptest.NewRequest(method, path, strings.NewReader(body))
\treq.Header.Set("Authorization", "Bearer "+token)
\tif body != "" {
\t\treq.Header.Set("Content-Type", "application/json")
\t}
\trec := httptest.NewRecorder()
\tsrv.Handler().ServeHTTP(rec, req)
\treturn rec
}

func TestTelegramConnectionIntegrationModesAndCapabilities(t *testing.T) {
\tsrv, _, controlDB, adminToken, _ := setupConnectionsTestEnv(t)

\tlegacy := authenticatedConnectionRequest(t, srv, adminToken, http.MethodGet, "/api/connections/conn-tg-1", "")
\tif legacy.Code != http.StatusOK {
\t\tt.Fatalf("get legacy Telegram connection: %d %s", legacy.Code, legacy.Body.String())
\t}
\tvar legacyDTO ConnectionDTO
\tif err := json.Unmarshal(legacy.Body.Bytes(), &legacyDTO); err != nil {
\t\tt.Fatal(err)
\t}
\tif legacyDTO.IntegrationMode != controlstore.TelegramIntegrationModeBot {
\t\tt.Fatalf("legacy mode=%q want bot", legacyDTO.IntegrationMode)
\t}
\tif legacyDTO.Capabilities == nil || legacyDTO.Capabilities.ChatDiscovery != telegram.DiscoveryObserved || legacyDTO.Capabilities.HistoryRecovery {
\t\tt.Fatalf("legacy capabilities=%+v", legacyDTO.Capabilities)
\t}

\tmt := authenticatedConnectionRequest(t, srv, adminToken, http.MethodPost, "/api/connections", `{"id":"conn-tg-mt","transport":"telegram","integrationMode":"mtproto","label":"phone account"}`)
\tif mt.Code != http.StatusCreated {
\t\tt.Fatalf("create MTProto connection: %d %s", mt.Code, mt.Body.String())
\t}
\tvar mtDTO ConnectionDTO
\tif err := json.Unmarshal(mt.Body.Bytes(), &mtDTO); err != nil {
\t\tt.Fatal(err)
\t}
\tif mtDTO.IntegrationMode != controlstore.TelegramIntegrationModeMTProto || mtDTO.Capabilities == nil || mtDTO.Capabilities.ChatDiscovery != telegram.DiscoveryFull || !mtDTO.Capabilities.HistoryRecovery || mtDTO.Capabilities.PrivacyModeStatus {
\t\tt.Fatalf("MTProto DTO=%+v", mtDTO)
\t}
\tvar storedMode string
\tvar ciphertext, nonce []byte
\tif err := controlDB.QueryRow(`SELECT integration_mode, encrypted_credential, credential_nonce FROM transport_connections WHERE id='conn-tg-mt'`).Scan(&storedMode, &ciphertext, &nonce); err != nil {
\t\tt.Fatal(err)
\t}
\tif storedMode != controlstore.TelegramIntegrationModeMTProto || len(ciphertext) == 0 || len(nonce) == 0 {
\t\tt.Fatalf("stored MTProto foundation state mode=%q ciphertext=%d nonce=%d", storedMode, len(ciphertext), len(nonce))
\t}

\twithToken := authenticatedConnectionRequest(t, srv, adminToken, http.MethodPost, "/api/connections", `{"id":"conn-tg-bad","transport":"telegram","integrationMode":"mtproto","label":"bad","token":"bot-secret"}`)
\tif withToken.Code != http.StatusBadRequest {
\t\tt.Fatalf("MTProto connection with bot token status=%d body=%s", withToken.Code, withToken.Body.String())
\t}
\tinvalid := authenticatedConnectionRequest(t, srv, adminToken, http.MethodPost, "/api/connections", `{"id":"conn-tg-invalid","transport":"telegram","integrationMode":"hybrid","label":"bad"}`)
\tif invalid.Code != http.StatusBadRequest {
\t\tt.Fatalf("invalid integration mode status=%d body=%s", invalid.Code, invalid.Body.String())
\t}
\tnonTelegram := authenticatedConnectionRequest(t, srv, adminToken, http.MethodPost, "/api/connections", `{"id":"conn-dc-mode","transport":"discord","integrationMode":"bot","label":"bad","token":"x"}`)
\tif nonTelegram.Code != http.StatusBadRequest {
\t\tt.Fatalf("non-Telegram integration mode status=%d body=%s", nonTelegram.Code, nonTelegram.Body.String())
\t}
}
''',
)

# Keep the tracker current; the workflow only commits this change if full tests/vet pass.
replace(
    "docs/TELEGRAM_MTPROTO_SUPPORT_PROGRESS.md",
    "- [ ] #102 — Telegram dual integration foundation: exclusive bot vs MTProto connection modes",
    "- [x] #102 — Telegram dual integration foundation: exclusive bot vs MTProto connection modes",
)
replace(
    "docs/TELEGRAM_MTPROTO_SUPPORT_PROGRESS.md",
    "## Completion rule\n",
    '''## Implementation log

### #102 — complete

- Added connection-level `integration_mode` metadata with restart-safe migration of existing Telegram rows to `bot`.
- Added explicit Bot API vs MTProto capability metadata without changing canonical `telegram` routing identity.
- Generic connection APIs expose mode/capabilities while credentials remain encrypted and omitted from DTOs.
- Startup/reload adapter selection is connection-metadata driven; the MTProto runtime hook is intentionally completed by #103.
- Existing Bot API connections remain the default and continue to use the existing adapter/token path.
- Verification: focused schema/API/capability/selection tests plus `go test ./...` and `go vet ./...` in the tested-change workflow.

## Completion rule
''',
)
