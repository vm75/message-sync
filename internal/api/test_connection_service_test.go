package api

import (
	"context"
	"errors"

	discord "github.com/vm75/message-sync/internal/transport/discord"
	telegram "github.com/vm75/message-sync/internal/transport/telegram"
)

// testConnectionService adapts individual transport fakes to the same explicit
// connection boundary used by the application. It is test-only; production
// code must always provide a real ConnectionService.
type testConnectionService struct {
	wa interface {
		Status(context.Context) WhatsAppStatus
		Pair(context.Context) (WhatsAppPairResponse, error)
		CancelPair(context.Context) error
		Logout(context.Context) error
		GetJoinedGroups(context.Context) ([]WhatsAppGroup, error)
	}
	dc discord.AdminService
	tg telegram.AdminService
}

func (s testConnectionService) ConnectionStatus(ctx context.Context, id string) (any, error) {
	switch id {
	case "conn-wa-1":
		if s.wa != nil {
			return s.wa.Status(ctx), nil
		}
	case "conn-dc-1":
		if s.dc != nil {
			return s.dc.AdminStatus(ctx), nil
		}
	case "conn-tg-1":
		if s.tg != nil {
			return s.tg.AdminStatus(ctx), nil
		}
	}
	return map[string]any{"id": id, "status": "stopped"}, nil
}

func (s testConnectionService) ConnectionDiscovery(ctx context.Context, id string) (any, error) {
	switch id {
	case "conn-wa-1":
		if s.wa != nil {
			return s.wa.GetJoinedGroups(ctx)
		}
	case "conn-dc-1":
		if s.dc != nil {
			return s.dc.DiscoverChannels(ctx)
		}
	case "conn-tg-1":
		if s.tg != nil {
			return s.tg.DiscoverChats(ctx)
		}
	}
	return nil, errors.New("connection unavailable")
}

func (s testConnectionService) WhatsAppPair(ctx context.Context, id string) (WhatsAppPairResponse, error) {
	if id == "conn-wa-1" && s.wa != nil {
		return s.wa.Pair(ctx)
	}
	return WhatsAppPairResponse{}, errors.New("connection unavailable")
}

func (s testConnectionService) WhatsAppCancelPair(ctx context.Context, id string) error {
	if id == "conn-wa-1" && s.wa != nil {
		return s.wa.CancelPair(ctx)
	}
	return errors.New("connection unavailable")
}

func (s testConnectionService) WhatsAppLogout(ctx context.Context, id string) error {
	if id == "conn-wa-1" && s.wa != nil {
		return s.wa.Logout(ctx)
	}
	return errors.New("connection unavailable")
}

func (testConnectionService) StopConnection(string) error { return nil }

func (s testConnectionService) ConnectionAdapter(id string) (any, bool) {
	switch id {
	case "conn-wa-1":
		return s.wa, s.wa != nil
	case "conn-dc-1":
		return s.dc, s.dc != nil
	case "conn-tg-1":
		return s.tg, s.tg != nil
	default:
		return nil, false
	}
}
