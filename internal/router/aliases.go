package router

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/vm75/message-sync/internal/transport"
)

func presentMentions(input []transport.Mention, anonymized bool) []transport.Mention {
	mentions := append([]transport.Mention(nil), input...)
	for i := range mentions {
		alias := mentions[i].Alias
		if anonymized {
			mentions[i].Name = alias
			if alias == "" {
				mentions[i].Name = "participant"
			}
		} else if alias != "" {
			mentions[i].Name = (transport.Sender{Alias: alias}).NameWithAlias(mentions[i].Name)
		}
	}
	return mentions
}

func (r *Router) prepareMentionAliases(ctx context.Context, setID string, input []transport.Mention) ([]transport.Mention, error) {
	mentions := append([]transport.Mention(nil), input...)
	for i := range mentions {
		mentions[i].Alias = ""
		actor := mentions[i].ActorID
		if actor == "" && isOpaqueActorID(mentions[i].RemoteID) {
			actor = mentions[i].RemoteID
		}
		if !isOpaqueActorID(actor) {
			continue
		}
		alias, err := r.store.AliasByActor(ctx, setID, actor)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("load mention alias: %w", err)
		}
		mentions[i].Alias = alias
	}
	return mentions, nil
}

// A route snapshot owns its alias namespace. Recheck current database ownership
// before provider creates so queued work cannot cross a later set reassignment.
func (r *Router) assertRouteSet(ctx context.Context, source, destination transport.EndpointID, setID string) error {
	var count int
	if err := r.store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM endpoints WHERE sync_set_id=? AND alias IN (?,?)`, setID, source, destination).Scan(&count); err != nil {
		return fmt.Errorf("verify route ownership: %w", err)
	}
	want := 2
	if source == destination {
		want = 1
	}
	if count != want {
		return transport.NewFailure(transport.FailureDestinationMissing, 0, errors.New("route ownership changed"))
	}
	return nil
}
