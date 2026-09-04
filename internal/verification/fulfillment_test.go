package verification

import "context"
import (
	"testing"
	"time"
)

type fakeWA struct {
	required bool
	member   bool
	pending  bool
	approved int
}

func (f *fakeWA) InviteLink(context.Context, string) (string, error)         { return "opaque-link", nil }
func (f *fakeWA) IsMember(context.Context, string, string) (bool, error)     { return f.member, nil }
func (f *fakeWA) JoinApprovalRequired(context.Context, string) (bool, error) { return f.required, nil }
func (f *fakeWA) PendingJoinRequests(context.Context, string) ([]PendingJoinRequest, error) {
	if !f.pending {
		return nil, nil
	}
	return []PendingJoinRequest{{Phone: "15551234567", RequestedAt: time.Unix(0, 0)}}, nil
}
func (f *fakeWA) ApproveJoinRequest(context.Context, string, string) error {
	f.approved++
	f.member = true
	return nil
}
func TestFulfillWhatsAppRequiresApprovalAndUsesInvite(t *testing.T) {
	f := &fakeWA{required: true}
	req := FulfillmentRequest{Transport: "whatsapp", EndpointAlias: "group", Phone: "+15551234567"}
	got, err := Fulfill(context.Background(), req, f, nil)
	if err != nil || got.State != "action_pending" || got.FailureClass != "invite_pending" {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	f.required = false
	got, err = Fulfill(context.Background(), req, f, nil)
	if err == nil || got.FailureClass != "join_approval_required" {
		t.Fatalf("missing approval gate result=%+v err=%v", got, err)
	}
	f.required, f.pending, f.member = true, true, false
	got, err = Fulfill(context.Background(), req, f, nil)
	if err != nil || got.State != "succeeded" || f.approved != 1 {
		t.Fatalf("join reconciliation result=%+v err=%v approved=%d", got, err, f.approved)
	}
}
