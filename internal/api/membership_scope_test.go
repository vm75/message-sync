package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestMembershipListsFilterBySyncSetBeforeLimit(t *testing.T) {
	srv, syncDB, controlDB, admin, _ := setupConnectionsTestEnv(t)
	if _, err := syncDB.Exec(`INSERT INTO sync_sets(id) VALUES('work'),('family');
 INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES ('a','whatsapp','conn-wa-1','1@g.us','work'),('b','whatsapp','conn-wa-1','2@g.us','family');`); err != nil {
		t.Fatal(err)
	}
	if _, err := controlDB.Exec(`INSERT INTO verification_pipelines(id,public_token,label,target_transport,endpoint_alias,enabled,creator_user_id,created_at,updated_at) VALUES ('pa','ta','Synthetic work','whatsapp','a',1,'user-admin',1,1),('pb','tb','Synthetic family','whatsapp','b',1,'user-admin',1,1);
 INSERT INTO membership_requests(id,pipeline_id,status,applicant_work_email,verification_state,created_at,updated_at) VALUES ('ra','pa','pending_admin','synthetic@example.com','verified',1,1);`); err != nil {
		t.Fatal(err)
	}
	// Many newer family requests must not crowd work requests out of a 500-row limit.
	tx, err := controlDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 501; index++ {
		if _, err := tx.Exec(`INSERT INTO membership_requests(id,pipeline_id,status,applicant_work_email,verification_state,created_at,updated_at) VALUES (printf('rb%d',?),'pb','pending_admin','synthetic@example.com','verified',2,2)`, index); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, id string
		count    int
	}{
		{"/api/verification/requests?syncSetId=work", "ra", 1},
		{"/api/verification/requests?syncSetId=family", "", 500},
		{"/api/verification/requests?syncSetId=empty", "", 0},
		{"/api/verification/requests?syncSetId=work&status=approved", "", 0},
		{"/api/verification/pipelines?syncSetId=work", "pa", 1},
		{"/api/verification/pipelines?syncSetId=family", "pb", 1},
		{"/api/verification/pipelines?syncSetId=empty", "", 0},
		{"/api/verification/pipelines", "", 2},
	} {
		response := request(t, srv, http.MethodGet, tc.path, nil, admin)
		if response.Code != 200 {
			t.Fatalf("%s: %d %s", tc.path, response.Code, response.Body.String())
		}
		var items []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		if len(items) != tc.count || (tc.id != "" && items[0].ID != tc.id) {
			t.Fatalf("%s: count=%d items=%+v", tc.path, len(items), items)
		}
	}
	for _, path := range []string{"/api/verification/pipelines?syncSetId=bad!", "/api/verification/requests?syncSetId=bad!"} {
		if response := request(t, srv, http.MethodGet, path, nil, admin); response.Code != 400 {
			t.Fatalf("invalid scope returned %d", response.Code)
		}
	}
}
