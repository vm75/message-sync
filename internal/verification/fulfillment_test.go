package verification

import "context"
import "testing"

type fakeWA struct{ adds int }

func (f *fakeWA) AddParticipant(context.Context, string, string) error   { f.adds++; return nil }
func (f *fakeWA) InviteLink(context.Context, string) (string, error)     { return "opaque-link", nil }
func (f *fakeWA) IsMember(context.Context, string, string) (bool, error) { return true, nil }
func (f *fakeWA) RotateInviteLink(context.Context, string) error         { return nil }
func TestFulfillWhatsAppIsSafeAndRepeatableAtBoundary(t *testing.T) {
	f := &fakeWA{}
	req := FulfillmentRequest{Transport: "whatsapp", EndpointAlias: "group", Phone: "+15551234567"}
	for i := 0; i < 2; i++ {
		got, err := Fulfill(context.Background(), req, f, nil)
		if err != nil || got.State != "succeeded" {
			t.Fatalf("result=%+v err=%v", got, err)
		}
	}
	if f.adds != 0 {
		t.Fatalf("already-member retries added participant %d times", f.adds)
	}
}
