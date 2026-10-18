package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// saveRoutingRows preserves the physical rows owning durable routing state.
// Only explicit removals and provider/account rebindings retire endpoint state.
func saveRoutingRows(ctx context.Context, tx *sql.Tx, cfg *Config, endpointSets map[string]string) error {
	rows, err := tx.QueryContext(ctx, `SELECT alias, transport, connection_id, remote_id FROM endpoints`)
	if err != nil {
		return fmt.Errorf("read endpoint bindings: %w", err)
	}
	var retired []string
	for rows.Next() {
		var alias, transport, connection, remote string
		if err := rows.Scan(&alias, &transport, &connection, &remote); err != nil {
			rows.Close()
			return fmt.Errorf("read endpoint binding: %w", err)
		}
		ep, exists := cfg.Endpoints[alias]
		if !exists || string(ep.Transport) != transport || ep.ConnectionID != connection || ep.RemoteID != remote {
			retired = append(retired, alias)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate endpoint bindings: %w", err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	// Retire all changed bindings first, permitting atomic provider-target swaps.
	for _, alias := range retired {
		if _, err := tx.ExecContext(ctx, `DELETE FROM endpoints WHERE alias=?`, alias); err != nil {
			return fmt.Errorf("retire endpoint binding: %w", err)
		}
	}
	retainedSets := make(map[string]bool, len(cfg.SyncSets))
	for _, set := range cfg.SyncSets {
		retainedSets[set.ID] = true
		settings, err := json.Marshal(cfg.SettingsForSet(set))
		if err != nil {
			return fmt.Errorf("encode sync-set settings: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sync_sets(id, anonymize_to_discord, settings_json) VALUES(?,?,?)
			ON CONFLICT(id) DO UPDATE SET anonymize_to_discord=excluded.anonymize_to_discord, settings_json=excluded.settings_json`, set.ID, set.AnonymizeToDiscord, string(settings)); err != nil {
			return fmt.Errorf("save sync set: %w", err)
		}
	}
	for alias, ep := range cfg.Endpoints {
		if _, err := tx.ExecContext(ctx, `INSERT INTO endpoints(alias, transport, connection_id, remote_id, sync_set_id, anonymize_incoming, anonymize_outgoing) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(alias) DO UPDATE SET sync_set_id=excluded.sync_set_id, anonymize_incoming=excluded.anonymize_incoming, anonymize_outgoing=excluded.anonymize_outgoing`, alias, string(ep.Transport), ep.ConnectionID, ep.RemoteID, endpointSets[alias], ep.AnonymizeIncoming, ep.AnonymizeOutgoing); err != nil {
			return fmt.Errorf("save endpoint: %w", err)
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT id FROM sync_sets`)
	if err != nil {
		return fmt.Errorf("read sync sets: %w", err)
	}
	var removed []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("read sync set: %w", err)
		}
		if !retainedSets[id] {
			removed = append(removed, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range removed {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sync_sets WHERE id=?`, id); err != nil {
			return fmt.Errorf("remove sync set: %w", err)
		}
	}
	return nil
}
