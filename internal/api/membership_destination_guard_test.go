package api

import (
	"context"
	"testing"
	"time"
)

func TestCompletedMembershipRequestDoesNotBlockDestinationChanges(t *testing.T) {
	srv := NewServer(Options{DB: setupTestDB(t)})
	now := time.Now().UnixMilli()
	if _, err := srv.controlDB.Exec(`
		INSERT INTO verification_pipelines(id,public_token,label,target_transport,endpoint_alias,enabled,creator_user_id,created_at,updated_at)
		VALUES ('pipeline','token','Community','whatsapp','group-a',1,'embedded-fixture',?,?);
		INSERT INTO membership_requests(id,pipeline_id,status,applicant_work_email,verification_state,fulfillment_state,created_at,updated_at)
		VALUES ('request','pipeline','approved','applicant@example.com','verified','succeeded',?,?)
	`, now, now, now, now); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if blocked, err := srv.pipelineHasOutstandingMembershipRequests(ctx, "pipeline"); err != nil || blocked {
		t.Fatalf("completed request blocked pipeline change: blocked=%v err=%v", blocked, err)
	}
	if blocked, err := srv.endpointHasOutstandingMembershipRequests(ctx, "group-a"); err != nil || blocked {
		t.Fatalf("completed request blocked endpoint change: blocked=%v err=%v", blocked, err)
	}

	if _, err := srv.controlDB.Exec(`UPDATE membership_requests SET fulfillment_state='action_pending' WHERE id='request'`); err != nil {
		t.Fatal(err)
	}
	if blocked, err := srv.pipelineHasOutstandingMembershipRequests(ctx, "pipeline"); err != nil || !blocked {
		t.Fatalf("pending fulfillment did not block pipeline change: blocked=%v err=%v", blocked, err)
	}
	if blocked, err := srv.endpointHasOutstandingMembershipRequests(ctx, "group-a"); err != nil || !blocked {
		t.Fatalf("pending fulfillment did not block endpoint change: blocked=%v err=%v", blocked, err)
	}
}
