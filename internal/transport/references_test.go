package transport

import "testing"

func TestRewriteCrossReferencesEscapingAndAuthorization(t *testing.T) {
	refs := []CrossReference{{Token: "@{g2/P1}", Endpoint: "g2", ActorID: "u_abcdefghij"}}
	render := func(ref CrossReference) (string, bool) {
		if ref.Endpoint != "g2" {
			return "", false
		}
		return "@native", true
	}
	cases := []struct{ source, want string }{
		{"Hi @{g2/P1}", "Hi @native"},
		{"Hi \\@{g2/P1}", "Hi \\@{g2/P1}"},
		{"Hi @{g2/P2}", "Hi @{g2/P2}"},
		{"Hi @{g2/P1} and @{g2/P1}", "Hi @native and @native"},
		{`\@{g2/P1} @{g2/P1}`, `\@{g2/P1} @native`},
		{`\\@{g2/P1}`, `\\@native`},
		{"Hi @{g1/P1}", "Hi @{g1/P1}"},
		{"@{unfinished Hi @{g2/P1}", "@{unfinished Hi @native"},
	}
	for _, tc := range cases {
		if got := RewriteCrossReferences(tc.source, refs, render); got != tc.want {
			t.Errorf("rewrite %q = %q, want %q", tc.source, got, tc.want)
		}
	}
}

func TestCrossReferencesPreservePostedTextUnlessNative(t *testing.T) {
	refs := []CrossReference{{Token: "@{g1/Alice Smith}", Endpoint: "g1", ActorID: "u_abcdefghij", Handle: "P1", Name: "Alice Smith", Pseudonym: "Rust Ocean Q7M5K"}}
	cases := []struct {
		name   string
		input  string
		native bool
		want   string
	}{
		{"known reference fallback", "Hi @{g1/Alice Smith}", false, "Hi @{g1/Alice Smith}"},
		{"native addressed group", "Hi @{g1/Alice Smith}", true, "Hi @native"},
		{"unknown stable handle", "Hi @{g1/P7}", false, "Hi @{g1/P7}"},
		{"unknown name", "Hi @{g1/Bob Private}", false, "Hi @{g1/Bob Private}"},
		{"escaped reference", "Hi \\@{g1/Alice Smith}", false, "Hi \\@{g1/Alice Smith}"},
		{"unrelated native name", "Hi @Alice", false, "Hi @Alice"},
		{"literal reference does not tag", "Hi @g1/Alice Smith", true, "Hi @g1/Alice Smith"},
		{"repeated references", "@{g1/Alice Smith}, @{g1/Alice Smith}", false, "@{g1/Alice Smith}, @{g1/Alice Smith}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notifications := 0
			got := RewriteCrossReferences(tc.input, refs, func(ref CrossReference) (string, bool) {
				if !tc.native {
					return "", false
				}
				notifications++
				return "@native", true
			})
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			wantNotifications := 0
			if tc.want == "Hi @native" {
				wantNotifications = 1
			}
			if notifications != wantNotifications {
				t.Fatalf("native references triggered %d notifications, want %d", notifications, wantNotifications)
			}
		})
	}
}

func TestReferenceDisplayLabelUsesPseudonymBeforeHandle(t *testing.T) {
	ref := CrossReference{
		Token: "@{g1/P1}", Endpoint: "g1", ActorID: "u_abcdefghij",
		Handle: "P1", Name: "Alice Smith", Pseudonym: "Rust Ocean Q7M5K",
	}
	for _, tc := range []struct {
		anon bool
		want string
	}{
		{true, "Rust Ocean Q7M5K"},
		{false, "Alice Smith"},
	} {
		if got := ReferenceDisplayLabel(ref, tc.anon); got != tc.want {
			t.Fatalf("alias with anonymized=%t: got %q want %q", tc.anon, got, tc.want)
		}
		if got := RewriteCrossReferences("Hi @{g1/P1}", []CrossReference{ref}, nil); got != "Hi @{g1/P1}" {
			t.Fatalf("formatted alias with anonymized=%t: %q", tc.anon, got)
		}
	}
	ref.Pseudonym = ""
	if got := ReferenceDisplayLabel(ref, true); got != "P1" {
		t.Fatalf("missing pseudonym should fall back to stable handle, got %q", got)
	}
}
