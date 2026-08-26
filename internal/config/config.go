package config

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	aliasPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	syncSetIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	jidPattern       = regexp.MustCompile(`^[0-9A-Za-z._-]+@g\.us$`)
)

func ValidateAlias(alias string) error {
	if !aliasPattern.MatchString(alias) {
		return fmt.Errorf("group alias %q must match %s", alias, aliasPattern.String())
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
		return fmt.Errorf("group jid %q is invalid WhatsApp group JID", jid)
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
	Groups          map[string]Group `json:"groups"`
	SyncSets        []SyncSet        `json:"syncSets"`
	Identity        Identity         `json:"identity"`
	Media           Media            `json:"media"`
	Recovery        Recovery         `json:"recovery"`
	Storage         Storage          `json:"storage"`
	Polls           Polls            `json:"polls"`
	WhatsAppCleanup WhatsAppCleanup  `json:"whatsappCleanup"`
}

type Group struct {
	JID string `json:"jid"`
}

type SyncSet struct {
	ID     string   `json:"id"`
	Groups []string `json:"groups"`
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
		Groups:   make(map[string]Group),
		SyncSets: make([]SyncSet, 0),
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
	)
	row := db.QueryRowContext(ctx, `
		SELECT username_mode, media_enabled, media_max_size_mb, recovery_enabled, recovery_max_age_hours, recovery_max_messages_per_group, storage_message_retention_days, poll_aggregation_trigger, whatsapp_chat_cleanup_enabled, whatsapp_chat_retention_days
		FROM global_config WHERE id = 1
	`)
	if err := row.Scan(&modeStr, &mediaEnabled, &maxSizeMB, &recEnabled, &maxAgeHours, &maxPerGroup, &retention, &aggTrigger, &whatsappCleanupEnabled, &whatsappCleanupDays); err != nil {
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
	}

	applyDefaults(cfg)

	groupRows, err := db.QueryContext(ctx, `SELECT alias, jid, sync_set_id FROM groups ORDER BY alias ASC`)
	if err != nil {
		return nil, fmt.Errorf("read groups: %w", err)
	}

	syncSetMap := make(map[string][]string)
	for groupRows.Next() {
		var alias, jid string
		var syncSetID sql.NullString
		if err := groupRows.Scan(&alias, &jid, &syncSetID); err != nil {
			groupRows.Close()
			return nil, fmt.Errorf("scan group: %w", err)
		}
		cfg.Groups[alias] = Group{JID: jid}
		if syncSetID.Valid && strings.TrimSpace(syncSetID.String) != "" {
			syncSetMap[syncSetID.String] = append(syncSetMap[syncSetID.String], alias)
		}
	}
	if err := groupRows.Err(); err != nil {
		groupRows.Close()
		return nil, fmt.Errorf("iterate groups: %w", err)
	}
	groupRows.Close()

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
		groups := syncSetMap[id]
		if groups == nil {
			groups = []string{}
		}
		cfg.SyncSets = append(cfg.SyncSets, SyncSet{
			ID:     id,
			Groups: groups,
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
		INSERT INTO global_config (id, username_mode, media_enabled, media_max_size_mb, recovery_enabled, recovery_max_age_hours, recovery_max_messages_per_group, storage_message_retention_days, poll_aggregation_trigger, whatsapp_chat_cleanup_enabled, whatsapp_chat_retention_days)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
			whatsapp_chat_retention_days = excluded.whatsapp_chat_retention_days
	`, string(cfg.Identity.UsernameMode), cfg.Media.Enabled, cfg.Media.MaxSizeMB, cfg.Recovery.Enabled, cfg.Recovery.MaxAgeHours, cfg.Recovery.MaxMessagesPerGroup, cfg.Storage.MessageRetentionDays, cfg.Polls.AggregationTrigger, cfg.WhatsAppCleanup.Enabled, cfg.WhatsAppCleanup.RetentionDays)
	if err != nil {
		return fmt.Errorf("save global_config: %w", err)
	}

	groupToSyncSet := make(map[string]string)
	for _, set := range cfg.SyncSets {
		for _, alias := range set.Groups {
			groupToSyncSet[alias] = set.ID
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM groups`); err != nil {
		return fmt.Errorf("delete old groups: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sync_sets`); err != nil {
		return fmt.Errorf("delete old sync_sets: %w", err)
	}

	for _, set := range cfg.SyncSets {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sync_sets (id) VALUES (?)`, set.ID); err != nil {
			return fmt.Errorf("insert sync_set %q: %w", set.ID, err)
		}
	}

	for alias, grp := range cfg.Groups {
		syncSetID := groupToSyncSet[alias]
		var syncSetVal any
		if syncSetID != "" {
			syncSetVal = syncSetID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO groups (alias, jid, sync_set_id) VALUES (?, ?, ?)`, alias, grp.JID, syncSetVal); err != nil {
			return fmt.Errorf("insert group %q: %w", alias, err)
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
}

func (c Config) Validate() error {
	if len(c.Groups) < 2 {
		return errors.New("at least two groups are required")
	}
	if len(c.SyncSets) == 0 {
		return errors.New("at least one sync set is required")
	}
	if !c.Identity.UsernameMode.IsValid() {
		return errors.New("identity.usernameMode must be push_name or hash")
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

	for alias, group := range c.Groups {
		if !aliasPattern.MatchString(alias) {
			return fmt.Errorf("group alias %q must match %s", alias, aliasPattern.String())
		}
		if strings.TrimSpace(group.JID) == "" {
			return fmt.Errorf("group %q has empty jid", alias)
		}
	}

	membership := make(map[string]string, len(c.Groups))
	for _, set := range c.SyncSets {
		if strings.TrimSpace(set.ID) == "" {
			return errors.New("sync set id is required")
		}
		if len(set.Groups) < 2 {
			return fmt.Errorf("sync set %q must contain at least two groups", set.ID)
		}
		seen := make(map[string]struct{}, len(set.Groups))
		for _, alias := range set.Groups {
			if _, ok := c.Groups[alias]; !ok {
				return fmt.Errorf("sync set %q references unknown group %q", set.ID, alias)
			}
			if _, duplicate := seen[alias]; duplicate {
				return fmt.Errorf("sync set %q repeats group %q", set.ID, alias)
			}
			seen[alias] = struct{}{}
			if previous, exists := membership[alias]; exists {
				return fmt.Errorf("group %q belongs to both %q and %q", alias, previous, set.ID)
			}
			membership[alias] = set.ID
		}
	}
	for alias := range c.Groups {
		if _, ok := membership[alias]; !ok {
			return fmt.Errorf("group %q is not assigned to a sync set", alias)
		}
	}
	return nil
}
