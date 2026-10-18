package store

import (
	"context"
	"encoding/hex"
	"errors"
)

// SavePollSourceOptionHashes preserves provider vote keys when forwarded option
// labels have been projected to aliases. No option text is stored here.
func (s *Store) SavePollSourceOptionHashes(ctx context.Context, canonicalID, endpointID string, hashes []string) error {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return err
	}
	if err := validateEndpoint(endpointID); err != nil {
		return err
	}
	for _, hash := range hashes {
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != 32 {
			return errors.New("invalid poll source option hash")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapDB("begin poll source hashes", err)
	}
	defer tx.Rollback()
	for index, hash := range hashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO poll_source_option_hashes(canonical_id,endpoint_id,option_index,option_hash) VALUES(?,?,?,?) ON CONFLICT(canonical_id,endpoint_id,option_index) DO UPDATE SET option_hash=excluded.option_hash`, canonicalID, endpointID, index, hash); err != nil {
			return wrapDB("save poll source hash", err)
		}
	}
	return tx.Commit()
}

func (s *Store) pollVoteOptionIndex(ctx context.Context, canonicalID, endpointID, hash string) (int, error) {
	var hasSource bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM poll_source_option_hashes WHERE canonical_id=? AND endpoint_id=?)`, canonicalID, endpointID).Scan(&hasSource); err != nil {
		return 0, wrapDB("read poll source mapping", err)
	}
	if !hasSource {
		return s.PollOptionIndexForWhatsAppHash(ctx, canonicalID, hash)
	}
	var index int
	err := s.db.QueryRowContext(ctx, `SELECT option_index FROM poll_source_option_hashes WHERE canonical_id=? AND endpoint_id=? AND option_hash=?`, canonicalID, endpointID, hash).Scan(&index)
	return index, wrapDB("resolve source poll option", err)
}
