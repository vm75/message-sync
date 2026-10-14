package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ParticipantAddress identifies an observed actor within exactly one endpoint.
// Handles are monotonically allocated per endpoint and never derived from names.
type ParticipantAddress struct {
	Endpoint string
	Handle   string
	ActorID  string
}

// ObserveParticipant allocates a durable, endpoint-scoped address for an HMAC
// actor ID. No provider ID, username, phone, or message content is stored.
func (s *Store) ObserveParticipant(ctx context.Context, endpoint, actorID string) (ParticipantAddress, error) {
	if s == nil || s.db == nil {
		return ParticipantAddress{}, errors.New("sync store is required")
	}
	if !endpointPattern.MatchString(endpoint) || !actorPattern.MatchString(actorID) {
		return ParticipantAddress{}, errors.New("invalid participant address")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ParticipantAddress{}, fmt.Errorf("begin participant observation: %w", err)
	}
	defer tx.Rollback()
	var ordinal int64
	err = tx.QueryRowContext(ctx,
		`SELECT ordinal FROM participant_addresses WHERE endpoint_id=? AND actor_id=?`,
		endpoint, actorID).Scan(&ordinal)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(ordinal), 0)+1 FROM participant_addresses WHERE endpoint_id=?`,
			endpoint).Scan(&ordinal)
		if err == nil {
			_, err = tx.ExecContext(ctx,
				`INSERT INTO participant_addresses(endpoint_id, actor_id, ordinal) VALUES(?, ?, ?)`,
				endpoint, actorID, ordinal)
		}
	}
	if err != nil {
		return ParticipantAddress{}, fmt.Errorf("observe participant: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ParticipantAddress{}, fmt.Errorf("commit participant observation: %w", err)
	}
	return ParticipantAddress{Endpoint: endpoint, Handle: "P" + strconv.FormatInt(ordinal, 10), ActorID: actorID}, nil
}

// ParticipantByHandle looks up a previously observed participant, never a
// display-name guess. Handles are case sensitive and canonical (P1, P2, ...).
func (s *Store) ParticipantByHandle(ctx context.Context, endpoint, handle string) (ParticipantAddress, error) {
	if s == nil || s.db == nil {
		return ParticipantAddress{}, errors.New("sync store is required")
	}
	if !endpointPattern.MatchString(endpoint) || len(handle) < 2 || handle[0] != 'P' || handle[1] == '0' {
		return ParticipantAddress{}, errors.New("invalid participant address")
	}
	ordinal, err := strconv.ParseInt(strings.TrimPrefix(handle, "P"), 10, 64)
	if err != nil || ordinal < 1 || "P"+strconv.FormatInt(ordinal, 10) != handle {
		return ParticipantAddress{}, errors.New("invalid participant address")
	}
	var actorID string
	if err := s.db.QueryRowContext(ctx,
		`SELECT actor_id FROM participant_addresses WHERE endpoint_id=? AND ordinal=?`,
		endpoint, ordinal).Scan(&actorID); err != nil {
		return ParticipantAddress{}, err
	}
	return ParticipantAddress{Endpoint: endpoint, Handle: handle, ActorID: actorID}, nil
}

// ParticipantByActor resolves an observed membership without revealing the
// provider-native identity or requiring a global person profile.
func (s *Store) ParticipantByActor(ctx context.Context, endpoint, actorID string) (ParticipantAddress, error) {
	if s == nil || s.db == nil {
		return ParticipantAddress{}, errors.New("sync store is required")
	}
	if !endpointPattern.MatchString(endpoint) || !actorPattern.MatchString(actorID) {
		return ParticipantAddress{}, errors.New("invalid participant address")
	}
	var ordinal int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT ordinal FROM participant_addresses WHERE endpoint_id=? AND actor_id=?`,
		endpoint, actorID).Scan(&ordinal); err != nil {
		return ParticipantAddress{}, err
	}
	return ParticipantAddress{Endpoint: endpoint, Handle: "P" + strconv.FormatInt(ordinal, 10), ActorID: actorID}, nil
}

// ParticipantsForEndpoint returns privacy-safe observed identities for one
// configured endpoint. Display names, phone numbers and provider IDs remain
// unavailable; callers may derive presentation pseudonyms from HMAC actors.
func (s *Store) ParticipantsForEndpoint(ctx context.Context, endpoint string) ([]ParticipantAddress, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("sync store is required")
	}
	if !endpointPattern.MatchString(endpoint) {
		return nil, errors.New("invalid participant endpoint")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT actor_id, ordinal FROM participant_addresses WHERE endpoint_id=? ORDER BY ordinal`, endpoint)
	if err != nil {
		return nil, fmt.Errorf("list participant addresses: %w", err)
	}
	defer rows.Close()
	var addresses []ParticipantAddress
	for rows.Next() {
		var actor string
		var ordinal int64
		if err := rows.Scan(&actor, &ordinal); err != nil {
			return nil, fmt.Errorf("scan participant address: %w", err)
		}
		addresses = append(addresses, ParticipantAddress{
			Endpoint: endpoint, Handle: "P" + strconv.FormatInt(ordinal, 10), ActorID: actor,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list participant addresses: %w", err)
	}
	return addresses, nil
}
