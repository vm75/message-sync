package transport

import "testing"

func TestRewriteCrossReferencesEscapingAndAuthorization(t *testing.T) {
	refs := []CrossReference{{Token: "@{g2/P1}", Endpoint: "g2", ActorID: "u_abcdefghij"}}
	render := func(ref CrossReference) (string, bool) {
		if ref.Endpoint != "g2" { return "", false }
		return "@native", true
	}
	cases := []struct{ source, want string }{
		{"Hi @{g2/P1}", "Hi @native"},
		{"Hi \\@{g2/P1}", "Hi \\@{g2/P1}"},
		{"Hi @{g2/P2}", "Hi @{g2/P2}"},
		{"Hi @{g2/P1} and @{g2/P1}", "Hi @native and @native"},
		{"Hi @{g1/P1}", "Hi @{g1/P1}"},
	}
	for _, tc := range cases {
		if got := RewriteCrossReferences(tc.source, refs, render); got != tc.want {
			t.Errorf("rewrite %q = %q, want %q", tc.source, got, tc.want)
		}
	}
}
