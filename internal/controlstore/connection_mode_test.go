package controlstore

import (
	"testing"
)

func TestValidateIntegrationMode(t *testing.T) {
	for _, tc := range []struct {
		transport string
		mode      string
		wantErr   bool
	}{
		{"telegram", "", false},
		{"telegram", "bot", false},
		{"telegram", "mtproto", false},
		{"telegram", "other", true},
		{"discord", "", false},
		{"discord", "managed", false},
		{"discord", "webhook", false},
		{"discord", "bot", true},
	} {
		if err := ValidateIntegrationMode(tc.transport, tc.mode); (err != nil) != tc.wantErr {
			t.Errorf("ValidateIntegrationMode(%q,%q) err=%v wantErr=%v", tc.transport, tc.mode, err, tc.wantErr)
		}
	}
}
