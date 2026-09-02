package verification

import (
	"context"
	"errors"
)

var ErrPermissionDenied = errors.New("permission denied")
var ErrDestinationMissing = errors.New("destination missing")
var ErrRateLimited = errors.New("rate limited")

type WhatsAppAdmin interface {
	AddParticipant(context.Context, string, string) error
	InviteLink(context.Context, string) (string, error)
	IsMember(context.Context, string, string) (bool, error)
	RotateInviteLink(context.Context, string) error
}
type DiscordAdmin interface {
	AssignRole(context.Context, string, string, string) error
}
type FulfillmentRequest struct{ Transport, EndpointAlias, RoleID, Phone, DiscordUserID string }
type FulfillmentResult struct{ State, FailureClass string }

// Fulfill performs one idempotent control-plane action. Platform identity is
// supplied transiently from control.db and never enters router state/logs.
func Fulfill(ctx context.Context, req FulfillmentRequest, wa WhatsAppAdmin, dc DiscordAdmin) (FulfillmentResult, error) {
	if req.Transport == "whatsapp" {
		if wa == nil {
			return FulfillmentResult{State: "failed", FailureClass: "destination_missing"}, ErrDestinationMissing
		}
		if member, err := wa.IsMember(ctx, req.EndpointAlias, req.Phone); err == nil && member {
			return FulfillmentResult{State: "succeeded"}, nil
		}
		if err := wa.AddParticipant(ctx, req.EndpointAlias, req.Phone); err == nil {
			return FulfillmentResult{State: "succeeded"}, nil
		} else if errors.Is(err, ErrPermissionDenied) {
			return FulfillmentResult{State: "failed", FailureClass: "permission_denied"}, err
		} else if errors.Is(err, ErrRateLimited) {
			return FulfillmentResult{State: "failed", FailureClass: "rate_limited"}, err
		}
		link, err := wa.InviteLink(ctx, req.EndpointAlias)
		if err != nil {
			return FulfillmentResult{State: "action_pending", FailureClass: "invite_unavailable"}, err
		}
		_ = link
		return FulfillmentResult{State: "action_pending", FailureClass: "invite_fallback"}, nil
	}
	if req.Transport == "discord" && dc != nil {
		if err := dc.AssignRole(ctx, req.EndpointAlias, req.RoleID, req.DiscordUserID); err == nil {
			return FulfillmentResult{State: "succeeded"}, nil
		} else if errors.Is(err, ErrPermissionDenied) {
			return FulfillmentResult{State: "failed", FailureClass: "permission_denied"}, err
		} else if errors.Is(err, ErrRateLimited) {
			return FulfillmentResult{State: "failed", FailureClass: "rate_limited"}, err
		}
	}
	return FulfillmentResult{State: "failed", FailureClass: "destination_missing"}, ErrDestinationMissing
}
