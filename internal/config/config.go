package config

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	aliasPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	connectionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	syncSetIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	jidPattern          = regexp.MustCompile(`^[0-9A-Za-z._-]+@g\.us$`)
	discordIDPattern    = regexp.MustCompile(`^[0-9]{1,20}$`)
	telegramIDPattern   = regexp.MustCompile(`^-[1-9][0-9]{0,18}$`)
)

const (
	MaxLocalPrefixBytes        = 64
	MaxWhatsAppDeviceNameBytes = 64
	DefaultWhatsAppDeviceName  = "message-sync"
)

func ValidateWhatsAppDeviceName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("WhatsApp device name cannot be empty")
	}
	if strings.ContainsAny(name, "\x00\r\n") {
		return errors.New("WhatsApp device name contains invalid control characters")
	}
	if len([]byte(name)) > MaxWhatsAppDeviceNameBytes {
		return fmt.Errorf("WhatsApp device name must be at most %d UTF-8 bytes", MaxWhatsAppDeviceNameBytes)
	}
	return nil
}

func ValidateLocalPrefix(prefix string) error {
	if strings.ContainsAny(prefix, "\x00\r\n") {
		return errors.New("local message prefix contains invalid control characters")
	}
	if prefix != "" && strings.TrimSpace(prefix) == "" {
		return errors.New("local message prefix must contain a non-whitespace character")
	}
	if len([]byte(prefix)) > MaxLocalPrefixBytes {
		return fmt.Errorf("local message prefix must be at most %d UTF-8 bytes", MaxLocalPrefixBytes)
	}
	return nil
}

func ValidateAlias(alias string) error {
	if !aliasPattern.MatchString(alias) {
		return fmt.Errorf("endpoint alias %q must match %s", alias, aliasPattern.String())
	}
	return nil
}

func ValidateConnectionID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("endpoint connection id is required")
	}
	if !connectionIDPattern.MatchString(id) {
		return fmt.Errorf("endpoint connection id %q must match %s", id, connectionIDPattern.String())
	}
	return nil
}

func ValidateSyncSetID(id string) error {
	if !syncSetIDPattern.MatchString(id) {
		return fmt.Errorf("sync set id %q must match %s", id, syncSetIDPattern.String())
	}
	return nil
}

func ValidateGroupJID(jid string) error {
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return errors.New("group jid is required")
	}
	if !jidPattern.MatchString(jid) {
		return errors.New("group jid is invalid WhatsApp group JID")
	}
	return nil
}

type Transport string

const (
	TransportWhatsApp Transport = "whatsapp"
	TransportDiscord  Transport = "discord"
	TransportTelegram Transport = "telegram"
)

func (t Transport) IsValid() bool {
	return t == TransportWhatsApp || t == TransportDiscord || t == TransportTelegram
}

func ValidateEndpointRemoteID(transport Transport, remoteID string) error {
	if !transport.IsValid() {
		return errors.New("endpoint transport must be whatsapp, discord, or telegram")
	}
	remoteID = strings.TrimSpace(remoteID)
	if remoteID == "" {
		return errors.New("endpoint remote id is required")
	}
	if strings.ContainsAny(remoteID, "\r\n\x00") {
		return errors.New("endpoint remote id contains invalid control characters")
	}
	if transport == TransportWhatsApp && !jidPattern.MatchString(remoteID) {
		return errors.New("endpoint remote id is invalid WhatsApp group JID")
	}
	if transport == TransportDiscord {
		if !discordIDPattern.MatchString(remoteID) {
			return errors.New("endpoint remote id is invalid Discord channel ID")
		}
		value, err := strconv.ParseUint(remoteID, 10, 64)
		if err != nil || value == 0 {
			return errors.New("endpoint remote id is invalid Discord channel ID")
		}
	}
	if transport == TransportTelegram {
		if !telegramIDPattern.MatchString(remoteID) {
			return errors.New("endpoint remote id is invalid Telegram group/supergroup chat ID")
		}
		value, err := strconv.ParseInt(remoteID, 10, 64)
		if err != nil || value >= 0 {
			return errors.New("endpoint remote id is invalid Telegram group/supergroup chat ID")
		}
	}
	return nil
}

type UsernameMode string

const (
	UsernameModePushName UsernameMode = "push_name"
	UsernameModeHash     UsernameMode = "hash"
)

func (m UsernameMode) IsValid() bool {
	return m == UsernameModePushName || m == UsernameModeHash
}

type Config struct {
	Endpoints          map[string]Endpoint `json:"endpoints"`
	SyncSets           []SyncSet           `json:"syncSets"`
	Identity           Identity            `json:"identity"`
	Media              Media               `json:"media"`
	Recovery           Recovery            `json:"recovery"`
	Storage            Storage             `json:"storage"`
	Polls              Polls               `json:"polls"`
	WhatsAppCleanup    WhatsAppCleanup     `json:"whatsappCleanup"`
	LocalPrefix        string              `json:"localPrefix"`
	WhatsAppDeviceName string              `json:"whatsappDeviceName"`
}

type Endpoint struct {
	Transport    Transport `json:"transport"`
	ConnectionID string    `json:"connectionId"`
	RemoteID     string    `json:"remoteId"`
}

type SyncSet struct {
	ID        string   `json:"id"`
	Endpoints []string `json:"endpoints"`
}

type Identity struct {
	UsernameMode UsernameMode `json:"usernameMode"`
}

type Media struct {
	Enabled   bool `json:"enabled"`
	MaxSizeMB int  `json:"maxSizeMB"`
}

type Recovery struct {
	Enabled             bool `json:"enabled"`
	MaxAgeHours         int  `json:"maxAgeHours"`
	MaxMessagesPerGroup int  `json:"maxMessagesPerGroup"`
}

type Storage struct {
	MessageRetentionDays int `json:"messageRetentionDays"`
}

type Polls struct {
	AggregationTrigger string `json:"aggregationTrigger"`
}

type WhatsAppCleanup struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retentionDays"`
}

func LoadRaw(ctx context.Context, db *sql.DB) (*Config, error) {
	if db == nil {
		return nil, errors.New("database connection is required")
	}

	cfg := &Config{
		Endpoints: make(map[string]Endpoint),
		SyncSets:  make([]SyncSet, 0),
	}

	var (
		modeStr                string
		mediaEnabled           bool
		maxSizeMB              int
		recEnabled             bool
		maxAgeHours            int
		maxPerGroup            int
		retention              int
		aggTrigger             string
		whatsappCleanupEnabled bool
		whatsappCleanupDays    int
		localPrefix            string
		whatsappDeviceName     string
	)
	row := db.QueryRowContext(ctx, `
		SELECT username_mode, media_enabled, media_max_size_mb, recovery_enabled, recovery_max_age_hours, recovery_max_messages_per_group, storage_message_retention_days, poll_aggregation_trigger, whatsapp_chat_cleanup_enabled, whatsapp_chat_retention_days, local_message_prefix, whatsapp_device_name
		FROM global_config WHERE id = 1
	`)
	if err := row.Scan(&modeStr, &mediaEnabled, &maxSizeMB, &recEnabled, &maxAgeHours, &maxPerGroup, &retention, &aggTrigger, &whatsappCleanupEnabled, &whatsappCleanupDays, &localPrefix, &whatsappDeviceName); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("read global_config: %w", err)
		}
	} else {
		cfg.Identity.UsernameMode = UsernameMode(modeStr)
		cfg.Media.Enabled = mediaEnabled
		cfg.Media.MaxSizeMB = maxSizeMB
		cfg.Recovery.Enabled = recEnabled
		cfg.Recovery.MaxAgeHours = maxAgeHours
		cfg.Recovery.MaxMessagesPerGroup = maxPerGroup
		cfg.Storage.MessageRetentionDays = retention
		cfg.Polls.AggregationTrigger = aggTrigger
		cfg.WhatsAppCleanup.Enabled = whatsappCleanupEnabled
		cfg.WhatsAppCleanup.RetentionDays = whatsappCleanupDays
		cfg.LocalPrefix = localPrefix
		cfg.WhatsAppDeviceName = whatsappDeviceName
	}

	applyDefaults(cfg)

	endpointRows, err := db.QueryContext(ctx, `SELECT alias, transport, connection_id, remote_id, sync_set_id FROM endpoints ORDER BY alias ASC`)
	if err != nil {
		return nil, fmt.Errorf("read endpoints: %w", err)
	}

	syncSetMap := make(map[string][]string)
	for endpointRows.Next() {
		var alias, transportName, connectionID, remoteID string
		var syncSetID sql.NullString
		if err := endpointRows.Scan(&alias, &transportName, &connectionID, &remoteID, &syncSetID); err != nil {
			endpointRows.Close()
			return nil, fmt.Errorf("scan endpoint: %w", err)
		}
		cfg.Endpoints[alias] = Endpoint{
			Transport:    Transport(transportName),
			ConnectionID: connectionID,
			RemoteID:     remoteID,
		}
		if syncSetID.Valid && strings.TrimSpace(syncSetID.String) != "" {
			syncSetMap[syncSetID.String] = append(syncSetMap[syncSetID.String], alias)
		}
	}
	if err := endpointRows.Err(); err != nil {
		endpointRows.Close()
		return nil, fmt.Errorf("iterate endpoints: %w", err)
	}
	endpointRows.Close()

	setRows, err := db.QueryContext(ctx, `SELECT id FROM sync_sets ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("read sync_sets: %w", err)
	}
	defer setRows.Close()

	for setRows.Next() {
		var id string
		if err := setRows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan sync_set: %w", err)
		}
		endpoints := syncSetMap[id]
		if endpoints == nil {
			endpoints = []string{}
		}
		cfg.SyncSets = append(cfg.SyncSets, SyncSet{
			ID:        id,
			Endpoints: endpoints,
		})
	}
	if err := setRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sync_sets: %w", err)
	}

	return cfg, nil
}

func Load(ctx context.Context, db *sql.DB) (*Config, error) {
	cfg, err := LoadRaw(ctx, db)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// MigrateTelegramEndpoint atomically replaces only the operational Telegram
// remote address for an existing endpoint. Alias and sync-set membership are
// intentionally left untouched so canonical routing identity does not change.
func MigrateTelegramEndpoint(ctx context.Context, db *sql.DB, alias, oldRemoteID, newRemoteID string) error {
	if db == nil {
		return errors.New("database connection is required")
	}
	if err := ValidateAlias(alias); err != nil {
		return err
	}
	oldRemoteID = strings.TrimSpace(oldRemoteID)
	newRemoteID = strings.TrimSpace(newRemoteID)
	if err := ValidateEndpointRemoteID(TransportTelegram, oldRemoteID); err != nil {
		return errors.New("old Telegram endpoint remote id is invalid")
	}
	if err := ValidateEndpointRemoteID(TransportTelegram, newRemoteID); err != nil {
		return errors.New("new Telegram endpoint remote id is invalid")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("begin Telegram endpoint migration")
	}
	defer tx.Rollback()

	var currentRemoteID string
	err = tx.QueryRowContext(ctx,
		`SELECT remote_id FROM endpoints WHERE alias = ? AND transport = ?`,
		alias, string(TransportTelegram),
	).Scan(&currentRemoteID)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("Telegram endpoint is not configured")
	}
	if err != nil {
		return errors.New("read Telegram endpoint addressing")
	}

	// Migration service messages may be replayed after Telegram or process
	// recovery. Treat a previously-applied migration as idempotent.
	if currentRemoteID == newRemoteID {
		if err := tx.Commit(); err != nil {
			return errors.New("commit Telegram endpoint migration")
		}
		return nil
	}
	if currentRemoteID != oldRemoteID {
		return errors.New("Telegram endpoint addressing changed concurrently")
	}

	result, err := tx.ExecContext(ctx,
		`UPDATE endpoints SET remote_id = ? WHERE alias = ? AND transport = ? AND remote_id = ?`,
		newRemoteID, alias, string(TransportTelegram), oldRemoteID,
	)
	if err != nil {
		return errors.New("update Telegram endpoint addressing")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return errors.New("update Telegram endpoint addressing")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("commit Telegram endpoint migration")
	}
	return nil
}

func Save(ctx context.Context, db *sql.DB, cfg *Config) error {
	if db == nil {
		return errors.New("database connection is required")
	}
	if cfg == nil {
		return errors.New("config is required")
	}
	applyDefaults(cfg)
	if err := cfg.Validate(); err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin save config transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO global_config (id, username_mode, media_enabled, media_max_size_mb, recovery_enabled, recovery_max_age_hours, recovery_max_messages_per_group, storage_message_retention_days, poll_aggregation_trigger, whatsapp_chat_cleanup_enabled, whatsapp_chat_retention_days, local_message_prefix, whatsapp_device_name)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			username_mode = excluded.username_mode,
			media_enabled = excluded.media_enabled,
			media_max_size_mb = excluded.media_max_size_mb,
			recovery_enabled = excluded.recovery_enabled,
			recovery_max_age_hours = excluded.recovery_max_age_hours,
			recovery_max_messages_per_group = excluded.recovery_max_messages_per_group,
			storage_message_retention_days = excluded.storage_message_retention_days,
			poll_aggregation_trigger = excluded.poll_aggregation_trigger,
			whatsapp_chat_cleanup_enabled = excluded.whatsapp_chat_cleanup_enabled,
			whatsapp_chat_retention_days = excluded.whatsapp_chat_retention_days,
			local_message_prefix = excluded.local_message_prefix,
			whatsapp_device_name = excluded.whatsapp_device_name
	`, string(cfg.Identity.UsernameMode), cfg.Media.Enabled, cfg.Media.MaxSizeMB, cfg.Recovery.Enabled, cfg.Recovery.MaxAgeHours, cfg.Recovery.MaxMessagesPerGroup, cfg.Storage.MessageRetentionDays, cfg.Polls.AggregationTrigger, cfg.WhatsAppCleanup.Enabled, cfg.WhatsAppCleanup.RetentionDays, cfg.LocalPrefix, cfg.WhatsAppDeviceName)
	if err != nil {
		return fmt.Errorf("save global_config: %w", err)
	}

	endpointToSyncSet := make(map[string]string)
	for _, set := range cfg.SyncSets {
		for _, alias := range set.Endpoints {
			endpointToSyncSet[alias] = set.ID
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM endpoints`); err != nil {
		return fmt.Errorf("delete old endpoints: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sync_sets`); err != nil {
		return fmt.Errorf("delete old sync_sets: %w", err)
	}

	for _, set := range cfg.SyncSets {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sync_sets (id) VALUES (?)`, set.ID); err != nil {
			return fmt.Errorf("insert sync_set %q: %w", set.ID, err)
		}
	}

	for alias, endpoint := range cfg.Endpoints {
		syncSetID := endpointToSyncSet[alias]
		if _, err := tx.ExecContext(ctx, `INSERT INTO endpoints (alias, transport, connection_id, remote_id, sync_set_id) VALUES (?, ?, ?, ?, ?)`, alias, string(endpoint.Transport), endpoint.ConnectionID, endpoint.RemoteID, syncSetID); err != nil {
			return fmt.Errorf("insert endpoint %q: %w", alias, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit save config: %w", err)
	}
	return nil
}

func applyDefaults(cfg *Config) {
	if cfg.Identity.UsernameMode == "" {
		cfg.Identity.UsernameMode = UsernameModePushName
	}
	if cfg.Media.MaxSizeMB == 0 {
		cfg.Media.MaxSizeMB = 100
	}
	if cfg.Recovery.MaxAgeHours == 0 {
		cfg.Recovery.MaxAgeHours = 24
	}
	if cfg.Recovery.MaxMessagesPerGroup == 0 {
		cfg.Recovery.MaxMessagesPerGroup = 200
	}
	if cfg.Storage.MessageRetentionDays == 0 {
		cfg.Storage.MessageRetentionDays = 90
	}
	if cfg.Polls.AggregationTrigger == "" {
		cfg.Polls.AggregationTrigger = "aggregate-response"
	}
	if cfg.WhatsAppCleanup.RetentionDays == 0 {
		cfg.WhatsAppCleanup.RetentionDays = 30
	}
	if strings.TrimSpace(cfg.WhatsAppDeviceName) == "" {
		cfg.WhatsAppDeviceName = DefaultWhatsAppDeviceName
	}
}

func (c Config) Validate() error {
	if len(c.Endpoints) < 2 {
		return errors.New("at least two endpoints are required")
	}
	if len(c.SyncSets) == 0 {
		return errors.New("at least one sync set is required")
	}
	if !c.Identity.UsernameMode.IsValid() {
		return errors.New("identity.usernameMode must be push_name or hash")
	}
	if err := ValidateLocalPrefix(c.LocalPrefix); err != nil {
		return err
	}
	if err := ValidateWhatsAppDeviceName(c.WhatsAppDeviceName); err != nil {
		return err
	}
	if c.Media.MaxSizeMB < 1 {
		return errors.New("media.maxSizeMB must be positive")
	}
	if c.Recovery.MaxAgeHours < 1 {
		return errors.New("recovery.maxAgeHours must be positive")
	}
	if c.Recovery.MaxMessagesPerGroup < 1 {
		return errors.New("recovery.maxMessagesPerGroup must be positive")
	}
	if c.Storage.MessageRetentionDays < 1 {
		return errors.New("storage.messageRetentionDays must be positive")
	}
	if c.WhatsAppCleanup.Enabled && c.WhatsAppCleanup.RetentionDays < 1 {
		return errors.New("whatsappCleanup.retentionDays must be positive")
	}

	remoteTargets := make(map[string]string, len(c.Endpoints))
	for alias, endpoint := range c.Endpoints {
		if err := ValidateAlias(alias); err != nil {
			return err
		}
		if err := ValidateConnectionID(endpoint.ConnectionID); err != nil {
			return fmt.Errorf("endpoint %q: %w", alias, err)
		}
		if err := ValidateEndpointRemoteID(endpoint.Transport, endpoint.RemoteID); err != nil {
			return fmt.Errorf("endpoint %q: %w", alias, err)
		}
		key := string(endpoint.Transport) + "\x00" + strings.TrimSpace(endpoint.RemoteID)
		if previous, exists := remoteTargets[key]; exists {
			return fmt.Errorf("endpoints %q and %q use the same remote target", previous, alias)
		}
		remoteTargets[key] = alias
	}

	membership := make(map[string]string, len(c.Endpoints))
	for _, set := range c.SyncSets {
		if err := ValidateSyncSetID(set.ID); err != nil {
			return err
		}
		if len(set.Endpoints) < 2 {
			return fmt.Errorf("sync set %q must contain at least two endpoints", set.ID)
		}
		seen := make(map[string]struct{}, len(set.Endpoints))
		for _, alias := range set.Endpoints {
			if _, ok := c.Endpoints[alias]; !ok {
				return fmt.Errorf("sync set %q references unknown endpoint %q", set.ID, alias)
			}
			if _, duplicate := seen[alias]; duplicate {
				return fmt.Errorf("sync set %q repeats endpoint %q", set.ID, alias)
			}
			seen[alias] = struct{}{}
			if previous, exists := membership[alias]; exists {
				return fmt.Errorf("endpoint %q belongs to both %q and %q", alias, previous, set.ID)
			}
			membership[alias] = set.ID
		}
	}
	for alias := range c.Endpoints {
		if _, ok := membership[alias]; !ok {
			return fmt.Errorf("endpoint %q is not assigned to a sync set", alias)
		}
	}
	return nil
}
