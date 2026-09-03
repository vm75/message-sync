package whatsapp

import (
	"context"
	"errors"
	"strings"

	"github.com/vm75/message-sync/internal/transport"
	"github.com/vm75/message-sync/internal/verification"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func (a *Adapter) membershipTarget(alias string) (types.JID, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	target, ok := a.targets[transport.EndpointID(alias)]
	if !ok {
		return types.EmptyJID, errors.New("membership endpoint unavailable")
	}
	return target, nil
}
func (a *Adapter) AddParticipant(ctx context.Context, alias, phone string) error {
	target, err := a.membershipTarget(alias)
	if err != nil {
		return verification.ErrDestinationMissing
	}
	phone = strings.TrimPrefix(strings.TrimSpace(phone), "+")
	if phone == "" {
		return errors.New("invalid participant")
	}
	_, err = a.client.UpdateGroupParticipants(ctx, target, []types.JID{types.NewJID(phone, types.DefaultUserServer)}, whatsmeow.ParticipantChangeAdd)
	return err
}
func (a *Adapter) InviteLink(ctx context.Context, alias string) (string, error) {
	target, err := a.membershipTarget(alias)
	if err != nil {
		return "", verification.ErrDestinationMissing
	}
	return a.client.GetGroupInviteLink(ctx, target, false)
}
func (a *Adapter) IsMember(ctx context.Context, alias, phone string) (bool, error) {
	target, err := a.membershipTarget(alias)
	if err != nil {
		return false, err
	}
	info, err := a.client.GetGroupInfo(ctx, target)
	if err != nil {
		return false, err
	}
	phone = strings.TrimPrefix(strings.TrimSpace(phone), "+")
	for _, p := range info.Participants {
		if p.JID.User == phone {
			return true, nil
		}
	}
	return false, nil
}
func (a *Adapter) RotateInviteLink(ctx context.Context, alias string) error {
	target, err := a.membershipTarget(alias)
	if err != nil {
		return err
	}
	_, err = a.client.GetGroupInviteLink(ctx, target, true)
	return err
}

// MultiAdmin dispatches WhatsApp membership fulfillment operations to the adapter
// owning the target endpoint alias without disclosing connection identity.
type MultiAdmin struct {
	getAdapters func() []verification.WhatsAppAdmin
}

func NewMultiAdmin(getAdapters func() []verification.WhatsAppAdmin) *MultiAdmin {
	return &MultiAdmin{getAdapters: getAdapters}
}

func (m *MultiAdmin) find(alias string) (verification.WhatsAppAdmin, error) {
	if m == nil || m.getAdapters == nil {
		return nil, verification.ErrDestinationMissing
	}
	for _, a := range m.getAdapters() {
		if hasEp, ok := a.(interface{ HasEndpoint(string) bool }); ok && hasEp.HasEndpoint(alias) {
			return a, nil
		}
	}
	return nil, verification.ErrDestinationMissing
}

func (m *MultiAdmin) AddParticipant(ctx context.Context, alias, phone string) error {
	a, err := m.find(alias)
	if err != nil {
		return err
	}
	return a.AddParticipant(ctx, alias, phone)
}

func (m *MultiAdmin) InviteLink(ctx context.Context, alias string) (string, error) {
	a, err := m.find(alias)
	if err != nil {
		return "", err
	}
	return a.InviteLink(ctx, alias)
}

func (m *MultiAdmin) IsMember(ctx context.Context, alias, phone string) (bool, error) {
	a, err := m.find(alias)
	if err != nil {
		return false, err
	}
	return a.IsMember(ctx, alias, phone)
}

func (m *MultiAdmin) RotateInviteLink(ctx context.Context, alias string) error {
	a, err := m.find(alias)
	if err != nil {
		return err
	}
	return a.RotateInviteLink(ctx, alias)
}
