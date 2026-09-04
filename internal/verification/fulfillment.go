package verification

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrPermissionDenied = errors.New("permission denied")
var ErrDestinationMissing = errors.New("destination missing")
var ErrRateLimited = errors.New("rate limited")

type WhatsAppAdmin interface {
	InviteLink(context.Context, string) (string, error)
	IsMember(context.Context, string, string) (bool, error)
	JoinApprovalRequired(context.Context, string) (bool, error)
	PendingJoinRequests(context.Context, string) ([]PendingJoinRequest, error)
	ApproveJoinRequest(context.Context, string, string) error
}
type PendingJoinRequest struct {
	Phone       string
	RequestedAt time.Time
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
		required, err := wa.JoinApprovalRequired(ctx, req.EndpointAlias)
		if err != nil {
			return FulfillmentResult{State: "failed", FailureClass: "destination_unavailable"}, err
		}
		if !required {
			return FulfillmentResult{State: "action_pending", FailureClass: "join_approval_required"}, errors.New("group join approval is required")
		}
		pending, err := wa.PendingJoinRequests(ctx, req.EndpointAlias)
		if err != nil {
			return FulfillmentResult{State: "action_pending", FailureClass: "pending_requests_unavailable"}, err
		}
		wanted := strings.TrimPrefix(strings.TrimSpace(req.Phone), "+")
		for _, candidate := range pending {
			if candidate.Phone != wanted {
				continue
			}
			if err := wa.ApproveJoinRequest(ctx, req.EndpointAlias, candidate.Phone); err != nil {
				if errors.Is(err, ErrPermissionDenied) {
					return FulfillmentResult{State: "failed", FailureClass: "permission_denied"}, err
				}
				if errors.Is(err, ErrRateLimited) {
					return FulfillmentResult{State: "action_pending", FailureClass: "rate_limited"}, err
				}
				return FulfillmentResult{State: "action_pending", FailureClass: "join_request_unavailable"}, err
			}
			member, err := wa.IsMember(ctx, req.EndpointAlias, req.Phone)
			if err != nil || !member {
				return FulfillmentResult{State: "action_pending", FailureClass: "membership_unconfirmed"}, err
			}
			return FulfillmentResult{State: "succeeded"}, nil
		}
		link, err := wa.InviteLink(ctx, req.EndpointAlias)
		if err != nil {
			return FulfillmentResult{State: "action_pending", FailureClass: "invite_unavailable"}, err
		}
		_ = link
		return FulfillmentResult{State: "action_pending", FailureClass: "invite_pending"}, nil
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
