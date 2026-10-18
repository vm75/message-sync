package store

import (
	"context"
	"errors"
	"time"
)

// PruneSyncSetRetention uses the canonical message's original routing mesh.
// Endpoint moves do not transfer another set's retention policy to old copies.
func (s *Store) PruneSyncSetRetention(ctx context.Context, policies map[string]int, batchSize int) (int64, error) {
	if batchSize <= 0 {
		batchSize = 1000
	}
	var total int64
	for setID, days := range policies {
		if days < 1 {
			return total, errors.New("retention days must be positive")
		}
		cutoff := unixMillis(time.Now().UTC().AddDate(0, 0, -days))
		for _, table := range []string{"suppressed_reactions", "suppressed_local_messages"} {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE created_at < ? AND endpoint_id IN (SELECT alias FROM endpoints WHERE sync_set_id=?)`, cutoff, setID); err != nil {
				return total, wrapDB("prune sync-set suppression", err)
			}
		}
		for {
			result, err := s.db.ExecContext(ctx, `DELETE FROM canonical_messages WHERE canonical_id IN (SELECT canonical_id FROM canonical_messages WHERE sync_set_id=? AND created_at<? ORDER BY created_at LIMIT ?)`, setID, cutoff, batchSize)
			if err != nil {
				return total, wrapDB("prune sync-set retention", err)
			}
			count, err := result.RowsAffected()
			if err != nil {
				return total, err
			}
			total += count
			if count < int64(batchSize) {
				break
			}
		}
	}
	// Orphan history has no active mesh policy; retain it for the standard 90 days.
	cutoff := unixMillis(time.Now().UTC().AddDate(0, 0, -90))
	for _, table := range []string{"suppressed_reactions", "suppressed_local_messages"} {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE created_at < ? AND NOT EXISTS (SELECT 1 FROM endpoints e JOIN sync_sets ss ON ss.id=e.sync_set_id WHERE e.alias=`+table+`.endpoint_id)`, cutoff); err != nil {
			return total, wrapDB("prune orphan suppression", err)
		}
	}
	for {
		result, err := s.db.ExecContext(ctx, `DELETE FROM canonical_messages WHERE canonical_id IN (SELECT canonical_id FROM canonical_messages WHERE created_at<? AND (sync_set_id IS NULL OR NOT EXISTS(SELECT 1 FROM sync_sets WHERE id=canonical_messages.sync_set_id)) LIMIT ?)`, cutoff, batchSize)
		if err != nil {
			return total, wrapDB("prune orphan retention", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += count
		if count < int64(batchSize) {
			break
		}
	}
	return total, nil
}
