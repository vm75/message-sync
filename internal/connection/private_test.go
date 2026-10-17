package connection

import (
	"context"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/transport"
)

type privateStreamAdapter struct {
	*fakeAdapter
	private chan transport.PrivateIncoming
}

func (a *privateStreamAdapter) PrivateEvents() <-chan transport.PrivateIncoming { return a.private }

func TestPrivateIngressIsSeparateAndBoundToOwningConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := NewManager(ctx, nil, nil, nil)
	defer manager.Close()
	adapter := &privateStreamAdapter{fakeAdapter: newFakeAdapter(), private: make(chan transport.PrivateIncoming, 3)}
	if err := manager.Register(ctx, "owner", config.TransportDiscord, adapter); err != nil {
		t.Fatal(err)
	}
	adapter.private <- transport.PrivateIncoming{ConnectionID: "forged", Inbox: transport.PrivateInbox{ConnectionID: "other"}, Text: "foreign"}
	adapter.private <- transport.PrivateIncoming{ConnectionID: "forged", Inbox: transport.PrivateInbox{ConnectionID: "owner"}, Text: "private body"}
	select {
	case in := <-manager.PrivateEvents():
		if in.ConnectionID != "owner" || in.Text != "private body" {
			t.Fatal("private event did not retain its real owner")
		}
	case <-time.After(time.Second):
		t.Fatal("private ingress stalled")
	}
	select {
	case <-manager.Events():
		t.Fatal("private content entered group channel")
	default:
	}
	adapter.events <- transport.Incoming{Endpoint: "group", Kind: "text", Text: "group body"}
	select {
	case in := <-manager.Events():
		if in.Endpoint != "group" || in.Text != "group body" {
			t.Fatal("group behavior changed")
		}
	case <-time.After(time.Second):
		t.Fatal("group ingress stalled")
	}
}
