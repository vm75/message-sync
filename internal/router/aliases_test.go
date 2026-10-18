package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
)

// Fake-adapter router fixtures intentionally use synthetic provider targets.
// Populate their real store ownership without invoking transport-edge validation.
func persistRouterConfig(t *testing.T, db *store.Store, cfg *config.Config) {
	t.Helper()
	if db == nil || cfg == nil {
		return
	}
	for _, set := range cfg.SyncSets {
		if _, err := db.DB().Exec(`INSERT INTO sync_sets(id) VALUES(?) ON CONFLICT(id) DO NOTHING`, set.ID); err != nil {
			t.Fatal(err)
		}
		for _, alias := range set.Endpoints {
			ep := cfg.Endpoints[alias]
			if _, err := db.DB().Exec(`INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES(?,?,?,?,?) ON CONFLICT(alias) DO UPDATE SET sync_set_id=excluded.sync_set_id`, alias, ep.Transport, ep.ConnectionID, ep.RemoteID, set.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestAssignedAliasPresentationAndMentionFallback(t *testing.T) {
	ctx := context.Background()
	r, db, _ := newTestRouter(t)
	author, err := db.ObserveSyncSetActor(ctx, "mesh", "u_abcdefghij")
	if err != nil {
		t.Fatal(err)
	}
	mentioned, err := db.ObserveSyncSetActor(ctx, "mesh", "u_234567abcd")
	if err != nil {
		t.Fatal(err)
	}
	inc := testIncoming("c1g1", "message")
	inc.Mentions = []transport.Mention{{RemoteID: mentioned.ActorID, Name: "Bob"}, {RemoteID: "u_aaaaaaaaaa", Name: "Unseen"}}
	inc.Text = "Ask @" + mentioned.ActorID + " and @u_aaaaaaaaaa"
	inc.QuotedText = "*_c1g2/Private Name_*: quoted"
	for _, anon := range []bool{false, true} {
		sender, mentions, text, quote, err := r.presentationForTarget(ctx, inc, routeTarget{Endpoint: "c1g2", AnonymizeToDiscord: anon})
		if err != nil {
			t.Fatal(err)
		}
		want := "Alice (" + author.Alias + ")"
		if anon {
			want = author.Alias
		}
		if got := senderPresentationUsername(sender); got != want {
			t.Fatalf("anonymous=%t: %q want %q", anon, got, want)
		}
		if !strings.Contains(text, mentioned.Alias) || len(mentions) != 2 {
			t.Fatalf("assigned mention lost: %q %+v", text, mentions)
		}
		if anon && (sender.PhoneNumber != "" || strings.Contains(text, "Bob") || strings.Contains(text, "Unseen") || strings.Contains(quote, "Private Name")) {
			t.Fatalf("anonymous identity leak: %+v %q %q", sender, text, quote)
		}
	}
	var count int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM sync_set_actors`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("mention allocated alias: %d %v", count, err)
	}
}

func TestAliasAllocationFailurePreventsCanonicalAndOutboundCreate(t *testing.T) {
	ctx := context.Background()
	r, db, fake := newTestRouter(t)
	if _, err := db.DB().Exec(`CREATE TRIGGER fail_alias BEFORE INSERT ON sync_set_actors BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := r.Handle(ctx, testIncoming("c1g1", "failed")); err == nil {
		t.Fatal("allocation failure was swallowed")
	}
	var count int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM canonical_messages`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial canonical state: %d %v", count, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sent) != 0 {
		t.Fatal("sent without an assigned alias")
	}
}

func TestUnavailableWhatsAppIdentityConsumesNoAlias(t *testing.T) {
	r, db, _ := newTestRouter(t)
	in := testIncoming("c1g1", "unverified")
	in.Sender.IdentityUnavailable = true
	if err := r.Handle(context.Background(), in); err == nil {
		t.Fatal("accepted unresolved canonical identity")
	}
	var next int64
	if err := db.DB().QueryRow(`SELECT next_ordinal FROM alias_sequence`).Scan(&next); err != nil || next != 1 {
		t.Fatalf("consumed ordinal %d %v", next, err)
	}
}

func TestBridgeEchoDoesNotAllocateOrRequireAnotherAuthorIdentity(t *testing.T) {
	r, db, fake := newTestRouter(t)
	ctx := context.Background()
	original := testIncoming("c1g1", "original")
	if err := r.Handle(ctx, original); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, fake, 2)
	r.Close() // ensure provider acceptance and copy persistence precede this known echo
	fake.mu.Lock()
	copy := fake.sent[0].ref
	fake.mu.Unlock()
	echo := testIncoming(copy.Endpoint, copy.RemoteMessageID)
	echo.FromSelf = true
	echo.Sender.OpaqueID = "u_aaaaaaaaaa"
	echo.Sender.IdentityUnavailable = true
	if err := r.Handle(ctx, echo); err != nil {
		t.Fatalf("existing echo blocked checkpoint progress: %v", err)
	}
	var count int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM sync_set_actors`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("echo enrolled an actor: %d %v", count, err)
	}
}

func TestReferenceLookupFailureStopsBeforeCanonicalCreate(t *testing.T) {
	r, db, fake := newTestRouter(t)
	ctx := context.Background()
	if _, err := db.ObserveSyncSetActor(ctx, "mesh", "u_234567abcd"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`UPDATE sync_set_actors SET alias='Azure Bear' WHERE actor_id='u_234567abcd'`); err != nil {
		t.Fatal(err)
	}
	in := testIncoming("c1g1", "bad-reference")
	in.Text = "Hi @{Azure Bear}"
	if err := r.Handle(ctx, in); err == nil || !strings.Contains(err.Error(), "resolve alias reference") {
		t.Fatalf("reference failure swallowed: %v", err)
	}
	var count int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM canonical_messages`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("reference failure left canonical state: %d %v", count, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sent) != 0 {
		t.Fatal("forwarded after failed reference lookup")
	}
}

func TestRouteSnapshotCannotDeliverIntoReassignedSyncSet(t *testing.T) {
	r, db, fake := newTestRouter(t)
	ctx := context.Background()
	in := testIncoming("c1g1", "moving-target")
	in.MediaLoader = func(context.Context) ([]byte, error) {
		_, err := db.DB().Exec(`INSERT INTO sync_sets(id) VALUES('other'); UPDATE endpoints SET sync_set_id='other' WHERE alias='c1g2'`)
		return nil, err
	}
	if err := r.Handle(ctx, in); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, fake, 1)
	// Close cancels lane contexts. Wait for durable rejection first so shutdown
	// cannot interrupt the database write being asserted here.
	var state, class string
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := db.DB().QueryRow(`SELECT state,COALESCE(failure_class,'') FROM delivery_operations WHERE endpoint_id='c1g2'`).Scan(&state, &class); err != nil {
			t.Fatal(err)
		}
		if state == store.DeliveryFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stale route did not reach terminal state: %q %q", state, class)
		}
		time.Sleep(time.Millisecond)
	}
	r.Close()
	fake.mu.Lock()
	if len(fake.sent) != 1 || fake.sent[0].outgoing.Endpoint != "c1g3" {
		t.Fatalf("cross-set delivery: %+v", fake.sent)
	}
	fake.mu.Unlock()
	if class != string(transport.FailureDestinationMissing) {
		t.Fatalf("stale route outcome: %q %q", state, class)
	}
}

func TestQuoteOnlyMentionRetainsItsAssignedAliasAndPrivacy(t *testing.T) {
	r, db, _ := newTestRouter(t)
	ctx := context.Background()
	for _, actor := range []string{"u_abcdefghij", "u_234567abcd"} {
		if _, err := db.ObserveSyncSetActor(ctx, "mesh", actor); err != nil {
			t.Fatal(err)
		}
	}
	in := testIncoming("c1g1", "quote-only")
	in.QuotedText = "Ask @u_234567abcd"
	in.QuotedMentions = []transport.Mention{{RemoteID: "u_234567abcd", Name: "Private Bob"}}
	for _, anon := range []bool{false, true} {
		_, _, _, quote, err := r.presentationForTarget(ctx, in, routeTarget{AnonymizeToDiscord: anon})
		if err != nil {
			t.Fatal(err)
		}
		want := "Ask @Private Bob (Pale Poppy)"
		if anon {
			want = "Ask @Pale Poppy"
		}
		if quote != want {
			t.Fatalf("anonymous=%t quote=%q want %q", anon, quote, want)
		}
	}
}

func TestNativePollMentionFieldsUseRestartSafeAliases(t *testing.T) {
	r, db, fake := newTestRouter(t)
	ctx := context.Background()
	if _, err := db.ObserveSyncSetActor(ctx, "mesh", "u_234567abcd"); err != nil {
		t.Fatal(err)
	}
	in := testIncoming("c1g1", "poll-mentions")
	in.Kind = "poll"
	in.Text = "Ask @u_234567abcd"
	in.Mentions = []transport.Mention{{RemoteID: "u_234567abcd", Name: "Private Bob"}}
	in.PollOptions = []string{"With @u_234567abcd", "Without"}
	in.PollSelectableCount = 1
	if err := r.Handle(ctx, in); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, fake, 2)
	r.Close()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	count := 0
	for _, sent := range fake.sent {
		if sent.outgoing.Kind != "poll" {
			continue
		}
		count++
		if sent.outgoing.SourceText != "Ask @Amber Badger" || sent.outgoing.PollOptions[0] != "With @Amber Badger" {
			t.Fatalf("poll metadata lost its alias: %+v", sent.outgoing)
		}
	}
	if count != 2 {
		t.Fatalf("poll fan-out=%d", count)
	}
	canonical, err := db.CanonicalForRemote(ctx, "c1g1", in.RemoteID)
	if err != nil {
		t.Fatal(err)
	}
	for _, vote := range []struct{ endpoint, actor, option string }{
		{"c1g1", "u_abcdefghij", "With @u_234567abcd"},
		{"c1g2", "u_234567abcd", "With @Amber Badger"},
	} {
		hash := sha256.Sum256([]byte(vote.option))
		if err := db.RecordPollVote(ctx, canonical, vote.endpoint, vote.actor, []string{hex.EncodeToString(hash[:])}, time.Now()); err != nil {
			t.Fatalf("vote from %s: %v", vote.endpoint, err)
		}
	}
	counts, err := db.GetPollAggregateCounts(ctx, canonical)
	if err != nil || counts[0] != 2 {
		t.Fatalf("original and projected votes diverged: %v %v", counts, err)
	}
}
