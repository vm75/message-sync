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
func (a *Adapter) InviteLink(ctx context.Context, alias string) (string, error) {
	target, err := a.membershipTarget(alias)
	if err != nil {
		return "", verification.ErrDestinationMissing
	}
	return a.client.GetGroupInviteLink(ctx, target, false)
}
func (a *Adapter) JoinApprovalRequired(ctx context.Context, alias string) (bool, error) {
	target, err := a.membershipTarget(alias)
	if err != nil {
		return false, verification.ErrDestinationMissing
	}
	info, err := a.client.GetGroupInfo(ctx, target)
	if err != nil {
		return false, err
	}
	return info.IsJoinApprovalRequired, nil
}
func (a *Adapter) PendingJoinRequests(ctx context.Context, alias string) ([]verification.PendingJoinRequest, error) {
	target, err := a.membershipTarget(alias)
	if err != nil {
		return nil, verification.ErrDestinationMissing
	}
	requests, err := a.client.GetGroupRequestParticipants(ctx, target)
	if err != nil {
		return nil, err
	}
	out := make([]verification.PendingJoinRequest, 0, len(requests))
	for _, request := range requests {
		if request.JID.IsEmpty() {
			continue
		}
		id := request.JID.String()
		phone := ""
		if request.JID.Server == types.DefaultUserServer {
			phone = request.JID.User
		} else if (request.JID.Server == types.HiddenUserServer || request.JID.Server == types.HostedLIDServer) && a.client != nil && a.client.Store != nil {
			if alt, err := a.client.Store.GetAltJID(ctx, request.JID); err == nil && !alt.IsEmpty() && alt.ToNonAD().Server == types.DefaultUserServer {
				phone = alt.ToNonAD().User
			}
		}
		out = append(out, verification.PendingJoinRequest{
			ID:          id,
			Phone:       phone,
			RequestedAt: request.RequestedAt,
		})
	}
	return out, nil
}
func (a *Adapter) ApproveJoinRequest(ctx context.Context, alias, participant string) error {
	target, err := a.membershipTarget(alias)
	if err != nil {
		return verification.ErrDestinationMissing
	}
	participant = strings.TrimSpace(participant)
	if participant == "" {
		return errors.New("invalid participant")
	}
	var jid types.JID
	if strings.Contains(participant, "@") {
		var parseErr error
		jid, parseErr = types.ParseJID(participant)
		if parseErr != nil || jid.IsEmpty() {
			return errors.New("invalid participant")
		}
	} else {
		phone := strings.TrimPrefix(participant, "+")
		if phone == "" {
			return errors.New("invalid participant")
		}
		jid = types.NewJID(phone, types.DefaultUserServer)
	}
	_, err = a.client.UpdateGroupRequestParticipants(ctx, target, []types.JID{jid}, whatsmeow.ParticipantChangeApprove)
	return err
}

func groupParticipantMatchesPhone(participant types.GroupParticipant, phone string) bool {
	return ParticipantMatches(context.Background(), participant, phone, nil)
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
	for _, p := range info.Participants {
		if ParticipantMatches(ctx, p, phone, a.resolveAltJID) {
			return true, nil
		}
	}
	return false, nil
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

func (m *MultiAdmin) InviteLink(ctx context.Context, alias string) (string, error) {
	a, err := m.find(alias)
	if err != nil {
		return "", err
	}
	return a.InviteLink(ctx, alias)
}
func (m *MultiAdmin) JoinApprovalRequired(ctx context.Context, alias string) (bool, error) {
	a, err := m.find(alias)
	if err != nil {
		return false, err
	}
	return a.JoinApprovalRequired(ctx, alias)
}
func (m *MultiAdmin) PendingJoinRequests(ctx context.Context, alias string) ([]verification.PendingJoinRequest, error) {
	a, err := m.find(alias)
	if err != nil {
		return nil, err
	}
	return a.PendingJoinRequests(ctx, alias)
}
func (m *MultiAdmin) ApproveJoinRequest(ctx context.Context, alias, phone string) error {
	a, err := m.find(alias)
	if err != nil {
		return err
	}
	return a.ApproveJoinRequest(ctx, alias, phone)
}

func (m *MultiAdmin) IsMember(ctx context.Context, alias, phone string) (bool, error) {
	a, err := m.find(alias)
	if err != nil {
		return false, err
	}
	return a.IsMember(ctx, alias, phone)
}
