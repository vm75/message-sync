package router

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/vm75/message-sync/internal/transport"
)

func TestAliasReferencesResolveOnlyAssignedActorsInSourceSet(t *testing.T) {
	ctx := context.Background()
	r, db, _ := newTestRouter(t)
	record, err := db.ObserveSyncSetActor(ctx, "mesh", "u_abcdefghij")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`INSERT INTO sync_sets(id) VALUES('other')`); err != nil {
		t.Fatal(err)
	}
	outside, err := db.ObserveSyncSetActor(ctx, "other", "u_234567abcd")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token string
		count int
	}{
		{"@{" + record.Alias + "}", 1}, {"@{" + strings.ToLower(record.Alias) + "}", 1},
		{"@{" + outside.Alias + "}", 0}, {`\@{` + record.Alias + "}", 0},
		{"@{c1g1/P1}", 0}, {"@{Alice}", 0}, {"@{Unknown Member}", 0},
	} {
		refs, err := r.crossReferences(ctx, "c1g2", "Hi "+tc.token)
		if err != nil || len(refs) != tc.count {
			t.Fatalf("%q: %+v %v", tc.token, refs, err)
		}
		if len(refs) > 0 && (refs[0].ActorID != record.ActorID || refs[0].Alias != record.Alias || refs[0].Token != tc.token) {
			t.Fatalf("changed reference: %+v", refs[0])
		}
	}
	resolved, err := r.ResolveParticipantReference(ctx, "@{"+record.Alias+"}")
	if err != nil || resolved != record {
		t.Fatalf("global DM resolver: %+v %v", resolved, err)
	}
	if _, err := r.ResolveParticipantReference(ctx, "prefix @{"+record.Alias+"}"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("accepted substring: %v", err)
	}
	unknown, err := r.crossReferences(ctx, transport.EndpointID("outside"), "@{"+record.Alias+"}")
	if err != nil || len(unknown) != 0 {
		t.Fatalf("unconfigured source: %+v %v", unknown, err)
	}
}

func TestAliasReferencesPreserveEscapeLimitAndDatabaseErrors(t *testing.T) {
	ctx := context.Background()
	r, db, _ := newTestRouter(t)
	record, err := db.ObserveSyncSetActor(ctx, "mesh", "u_abcdefghij")
	if err != nil {
		t.Fatal(err)
	}
	token := "@{" + record.Alias + "}"
	refs, err := r.crossReferences(ctx, "c1g1", strings.Repeat(token+" ", 50))
	if err != nil || len(refs) != maxCrossReferences {
		t.Fatalf("reference limit=%d %v", len(refs), err)
	}
	refs, err = r.crossReferences(ctx, "c1g1", `\`+token+" "+token)
	if err != nil || len(refs) != 1 {
		t.Fatalf("escape: %+v %v", refs, err)
	}
	// Authorization of a valid occurrence must not activate an identical token
	// nested inside malformed syntax elsewhere in the same message.
	body := "@{" + token + "} and " + token
	refs, err = r.crossReferences(ctx, "c1g1", body)
	if err != nil || len(refs) != 1 {
		t.Fatalf("nested references: %+v %v", refs, err)
	}
	rendered := transport.RewriteCrossReferences(body, refs, func(transport.CrossReference) (string, bool) { return "@native", true })
	if rendered != "@{"+token+"} and @native" {
		t.Fatalf("nested token triggered a tag: %q", rendered)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.crossReferences(ctx, "c1g1", token); err == nil {
		t.Fatal("database failure treated as unknown alias")
	}
}
