package verification

import "testing"

func TestAssessIsAdvisory(t *testing.T) {
	cases := []struct {
		email, linkedin string
		evidence        bool
		domain, result  string
	}{
		{"person@gmail.com", "", false, "PERSONAL_OR_FREE", "LOW"},
		{"person@example.org", "https://www.linkedin.com/in/example", true, "WORK", "MEDIUM"},
		{"person@example.org", "http://linkedin.com/in/example", true, "WORK", "UNVERIFIED"},
	}
	for _, tc := range cases {
		got := Assess(tc.email, tc.linkedin, tc.evidence)
		if got.DomainClass != tc.domain || got.Result != tc.result {
			t.Fatalf("Assess(%q)=%+v", tc.email, got)
		}
	}
}
