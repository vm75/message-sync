package router

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/vm75/message-sync/internal/transport"
)

func TestGroupHelpStaysLocalAndDoesNotEnrollAuthors(t *testing.T) {
	for _, fromSelf := range []bool{false, true} {
		t.Run(map[bool]string{false: "participant", true: "linked-device"}[fromSelf], func(t *testing.T) {
			r, db, fake := newTestRouter(t)
			cfg := testConfig()
			cfg.LocalPrefix = "!only"
			cfg.Polls.AggregationTrigger = "/votes"
			if err := r.UpdateConfig(cfg); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			in := testIncoming("c1g1", "help-command")
			in.Text = "/help"
			in.FromSelf = fromSelf
			in.Sender.IdentityUnavailable = true
			if err := r.Handle(ctx, in); err != nil {
				t.Fatal(err)
			}
			if len(fake.sent) != 1 || fake.sent[0].outgoing.Endpoint != "c1g1" {
				t.Fatalf("help fanned out: %+v", fake.sent)
			}
			for _, want := range []string{"/help", "/dm", "/start", "/local", "/summary", "!only", "/votes"} {
				if !strings.Contains(fake.sent[0].outgoing.Text, want) {
					t.Fatalf("help omitted %s", want)
				}
			}
			if _, err := db.CanonicalForRemote(ctx, "c1g1", in.RemoteID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("help canonicalized: %v", err)
			}
			var enrolled int
			if err := db.DB().QueryRow(`SELECT COUNT(*) FROM sync_set_actors`).Scan(&enrolled); err != nil || enrolled != 0 {
				t.Fatalf("help enrolled author: %d %v", enrolled, err)
			}
			if err := r.Handle(ctx, in); err != nil {
				t.Fatal(err)
			}
			echo := testIncoming("c1g1", fake.sent[0].ref.RemoteMessageID)
			echo.FromSelf = true
			echo.Text = fake.sent[0].outgoing.Text
			if err := r.Handle(ctx, echo); err != nil {
				t.Fatal(err)
			}
			if len(fake.sent) != 1 {
				t.Fatal("help or its echo replayed")
			}
		})
	}
}

func TestBuiltinLocalSuppressesMediaWithoutConfiguredPrefixes(t *testing.T) {
	r, db, fake := newTestRouter(t)
	ctx := context.Background()
	in := testIncoming("c1g1", "builtin-local")
	in.Text = "/local keep private"
	in.Kind = "image"
	in.MediaLoader = func(context.Context) ([]byte, error) { t.Fatal("local media was loaded"); return nil, nil }
	if err := r.Handle(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Kind = "edit"
	in.RemoteID = "local-edit"
	in.Text = "now without prefix"
	in.ReplyTo = &transport.MessageRef{Endpoint: "c1g1", RemoteMessageID: "builtin-local"}
	if err := r.Handle(ctx, in); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 0 {
		t.Fatal("local message or later edit forwarded")
	}
	if _, err := db.CanonicalForRemote(ctx, "c1g1", "builtin-local"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("local canonicalized: %v", err)
	}
}
