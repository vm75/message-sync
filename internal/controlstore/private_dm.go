package controlstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/vm75/message-sync/internal/transport"
)

const PrivateDMReplyTTL = 24 * time.Hour
const privateDMRecordLimit = 100000

// PrivateDMSide contains only routing identity. Native inbox tokens are always
// encrypted by PrivateDMStore; no private message content is accepted here.
type PrivateDMSide struct {
	Endpoint string
	Binding  string
	ActorID  string
	Inbox    transport.PrivateInbox
}

type PrivateDMMapping struct {
	ConversationID string
	SyncSetID      string
	Author         PrivateDMSide
	Recipient      PrivateDMSide
}

type PrivateDMCleanup struct {
	Key      string
	Copy     transport.PrivateCopy
	Attempts int
}

// PrivateDMStore has a separate cryptographic domain from bot credentials.
// Keyed lookup digests bind replies to the owning connection and exact inbox.
type PrivateDMStore struct {
	db        *sql.DB
	cipher    *CredentialCipher
	lookupKey []byte
}

func NewPrivateDMStore(db *sql.DB, secret []byte) (*PrivateDMStore, error) {
	if db == nil || len(secret) < 32 {
		return nil, errors.New("private DM store requires database and identity secret")
	}
	derive := func(domain string) []byte {
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte("message-sync-private-dm-v1/" + domain))
		return mac.Sum(nil)
	}
	cipher, err := NewCredentialCipher(derive("encryption"))
	if err != nil {
		return nil, err
	}
	return &PrivateDMStore{db: db, cipher: cipher, lookupKey: derive("lookup")}, nil
}

func (s *PrivateDMStore) key(domain string, parts ...any) string {
	encoded, _ := json.Marshal(parts)
	mac := hmac.New(sha256.New, s.lookupKey)
	_, _ = mac.Write([]byte(domain + "\x00"))
	_, _ = mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *PrivateDMStore) inboxKey(domain string, inbox transport.PrivateInbox, messageID string) string {
	return s.key(domain, inbox.ConnectionID, inbox.Transport, inbox.OwnerID, inbox.ChatID, messageID)
}

func (s *PrivateDMStore) PutReply(ctx context.Context, messageID string, mapping PrivateDMMapping, now time.Time) error {
	if messageID == "" || mapping.Recipient.Inbox.ChatID == "" || len(mapping.Recipient.Inbox.Token) == 0 {
		return errors.New("invalid private DM reply mapping")
	}
	plain, err := json.Marshal(mapping)
	if err != nil {
		return errors.New("encode private DM reply mapping")
	}
	encrypted, nonce, err := s.cipher.Encrypt(plain)
	if err != nil {
		return errors.New("encrypt private DM reply mapping")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO private_dm_reply_mappings(lookup_key,ciphertext,nonce,created_at,expires_at) SELECT ?,?,?,?,? WHERE (SELECT COUNT(*) FROM private_dm_reply_mappings)<?`, s.inboxKey("reply", mapping.Recipient.Inbox, messageID), encrypted, nonce, now.UnixMilli(), now.Add(PrivateDMReplyTTL).UnixMilli(), privateDMRecordLimit)
	if err != nil {
		return errors.New("persist private DM reply mapping")
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errors.New("private DM reply capacity unavailable")
	}
	return nil
}

func (s *PrivateDMStore) ReplyCapacity(ctx context.Context) bool {
	var count int
	return s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM private_dm_reply_mappings`).Scan(&count) == nil && count < privateDMRecordLimit
}

func (s *PrivateDMStore) Reply(ctx context.Context, inbox transport.PrivateInbox, messageID string, now time.Time) (PrivateDMMapping, bool, error) {
	var mapping PrivateDMMapping
	var encrypted, nonce []byte
	err := s.db.QueryRowContext(ctx, `SELECT ciphertext,nonce FROM private_dm_reply_mappings WHERE lookup_key=? AND expires_at>?`, s.inboxKey("reply", inbox, messageID), now.UnixMilli()).Scan(&encrypted, &nonce)
	if errors.Is(err, sql.ErrNoRows) {
		return mapping, false, nil
	}
	if err != nil {
		return mapping, false, errors.New("read private DM reply mapping")
	}
	plain, err := s.cipher.Decrypt(encrypted, nonce)
	if err != nil {
		return mapping, false, errors.New("decrypt private DM reply mapping")
	}
	if err := json.Unmarshal(plain, &mapping); err != nil {
		return mapping, false, errors.New("decode private DM reply mapping")
	}
	return mapping, true, nil
}

// AcceptIncoming prevents duplicate provider creates, including after restart.
// Failed and ambiguous creates are deliberately not automatically retried.
func (s *PrivateDMStore) AcceptIncoming(ctx context.Context, inbox transport.PrivateInbox, messageID string, now time.Time) (bool, error) {
	if messageID == "" {
		return false, errors.New("private message ID is required")
	}
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO private_dm_seen_messages(lookup_key,expires_at) SELECT ?,? WHERE (SELECT COUNT(*) FROM private_dm_seen_messages)<?`, s.inboxKey("incoming", inbox, messageID), now.Add(PrivateDMReplyTTL).UnixMilli(), privateDMRecordLimit)
	if err != nil {
		return false, errors.New("record private DM input")
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// ObserveInbox keeps at most 10,000 verified, encrypted operational inboxes for
// 24 hours. It is not a contact directory and contains no profile or content.
func (s *PrivateDMStore) ObserveInbox(ctx context.Context, actor string, inbox transport.PrivateInbox, now time.Time) error {
	plain, err := json.Marshal(inbox)
	if err != nil {
		return errors.New("encode private inbox")
	}
	encrypted, nonce, err := s.cipher.Encrypt(plain)
	if err != nil {
		return errors.New("encrypt private inbox")
	}
	key := s.key("inbox", inbox.ConnectionID, inbox.Transport, actor)
	_, err = s.db.ExecContext(ctx, `INSERT INTO private_dm_inboxes(lookup_key,ciphertext,nonce,expires_at) SELECT ?,?,?,? WHERE (SELECT COUNT(*) FROM private_dm_inboxes)<10000 OR EXISTS(SELECT 1 FROM private_dm_inboxes WHERE lookup_key=?) ON CONFLICT(lookup_key) DO UPDATE SET ciphertext=excluded.ciphertext,nonce=excluded.nonce,expires_at=excluded.expires_at`, key, encrypted, nonce, now.Add(PrivateDMReplyTTL).UnixMilli(), key)
	if err != nil {
		return errors.New("persist private inbox")
	}
	return nil
}

func (s *PrivateDMStore) Inbox(ctx context.Context, connectionID, platform, actor string, now time.Time) (transport.PrivateInbox, bool, error) {
	var inbox transport.PrivateInbox
	var encrypted, nonce []byte
	err := s.db.QueryRowContext(ctx, `SELECT ciphertext,nonce FROM private_dm_inboxes WHERE lookup_key=? AND expires_at>?`, s.key("inbox", connectionID, platform, actor), now.UnixMilli()).Scan(&encrypted, &nonce)
	if errors.Is(err, sql.ErrNoRows) {
		return inbox, false, nil
	}
	if err != nil {
		return inbox, false, errors.New("read private inbox")
	}
	plain, err := s.cipher.Decrypt(encrypted, nonce)
	if err != nil {
		return inbox, false, errors.New("decrypt private inbox")
	}
	if json.Unmarshal(plain, &inbox) != nil {
		return inbox, false, errors.New("decode private inbox")
	}
	return inbox, true, nil
}

func (s *PrivateDMStore) QueueCleanup(ctx context.Context, copy transport.PrivateCopy, after, now time.Time) error {
	// Discord and Bot API deletion is shared with the participant. Never queue
	// those operations as an approximation of account-local housekeeping.
	if copy.Transport != "whatsapp" || len(copy.Token) == 0 {
		return nil
	}
	plain, err := json.Marshal(copy)
	if err != nil {
		return errors.New("encode private DM cleanup")
	}
	encrypted, nonce, err := s.cipher.Encrypt(plain)
	if err != nil {
		return errors.New("encrypt private DM cleanup")
	}
	key := s.key("cleanup", copy.ConnectionID, copy.Transport, copy.OwnerID, copy.Token)
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO private_dm_cleanup_candidates(candidate_key,ciphertext,nonce,created_at,cleanup_after,expires_at,next_attempt_at) SELECT ?,?,?,?,?,?,? WHERE (SELECT COUNT(*) FROM private_dm_cleanup_candidates)<?`, key, encrypted, nonce, now.UnixMilli(), after.UnixMilli(), after.Add(72*time.Hour).UnixMilli(), after.UnixMilli(), privateDMRecordLimit)
	if err != nil {
		return errors.New("persist private DM cleanup")
	}
	n, err := result.RowsAffected()
	if err != nil {
		return errors.New("persist private DM cleanup")
	}
	if n == 0 {
		var existing bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM private_dm_cleanup_candidates WHERE candidate_key=?)`, key).Scan(&existing); err != nil || !existing {
			return errors.New("private DM cleanup capacity unavailable")
		}
	}
	return nil
}

func (s *PrivateDMStore) DueCleanup(ctx context.Context, now time.Time, limit int) ([]PrivateDMCleanup, error) {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT candidate_key,ciphertext,nonce,attempts FROM private_dm_cleanup_candidates WHERE attempts<3 AND cleanup_after<=? AND next_attempt_at<=? AND expires_at>? ORDER BY cleanup_after LIMIT ?`, now.UnixMilli(), now.UnixMilli(), now.UnixMilli(), limit)
	if err != nil {
		return nil, errors.New("read private DM cleanup")
	}
	defer rows.Close()
	var candidates []PrivateDMCleanup
	for rows.Next() {
		var candidate PrivateDMCleanup
		var encrypted, nonce []byte
		if err := rows.Scan(&candidate.Key, &encrypted, &nonce, &candidate.Attempts); err != nil {
			return nil, errors.New("scan private DM cleanup")
		}
		plain, err := s.cipher.Decrypt(encrypted, nonce)
		if err != nil {
			return nil, errors.New("decrypt private DM cleanup")
		}
		if err := json.Unmarshal(plain, &candidate.Copy); err != nil {
			return nil, errors.New("decode private DM cleanup")
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// BeginCleanup durably counts the attempt before any provider call, so a
// timeout or process crash cannot reset the bounded retry budget.
func (s *PrivateDMStore) BeginCleanup(ctx context.Context, candidate PrivateDMCleanup, now time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE private_dm_cleanup_candidates SET attempts=attempts+1,next_attempt_at=? WHERE candidate_key=? AND attempts=? AND attempts<3 AND next_attempt_at<=? AND expires_at>?`, now.Add(time.Hour).UnixMilli(), candidate.Key, candidate.Attempts, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return false, errors.New("claim private DM cleanup")
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *PrivateDMStore) FinishCleanup(ctx context.Context, candidate PrivateDMCleanup, succeeded bool) error {
	var err error
	if succeeded || candidate.Attempts >= 2 {
		_, err = s.db.ExecContext(ctx, `DELETE FROM private_dm_cleanup_candidates WHERE candidate_key=?`, candidate.Key)
	}
	if err != nil {
		return errors.New("finish private DM cleanup")
	}
	return nil
}

func (s *PrivateDMStore) Prune(ctx context.Context, now time.Time, limit int) error {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	for _, table := range []string{"private_dm_reply_mappings", "private_dm_seen_messages", "private_dm_cleanup_candidates", "private_dm_inboxes"} {
		query := fmt.Sprintf(`DELETE FROM %s WHERE rowid IN (SELECT rowid FROM %s WHERE expires_at<=? LIMIT ?)`, table, table)
		for batch := 0; batch < privateDMRecordLimit/limit+1; batch++ {
			result, err := s.db.ExecContext(ctx, query, now.UnixMilli(), limit)
			if err != nil {
				return errors.New("prune private DM metadata")
			}
			n, err := result.RowsAffected()
			if err != nil {
				return errors.New("prune private DM metadata")
			}
			if n < int64(limit) {
				break
			}
		}
	}
	return nil
}
