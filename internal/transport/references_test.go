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
		{"Hi @{g1/P1}", "Hi @{g1/P1}"},
	}
	for _, tc := range cases {
		if got := RewriteCrossReferences(tc.source, refs, render); got != tc.want {
			t.Errorf("rewrite %q = %q, want %q", tc.source, got, tc.want)
		}
	}
}

func TestFormatCrossReferencesFallbackAnonymizationAndNative(t *testing.T) {
	refs := []CrossReference{{Token: "@{g1/Alice Smith}", Endpoint: "g1", ActorID: "u_abcdefghij", Handle: "P1", Name: "Alice Smith", Pseudonym: "Rust Ocean Q7M5K"}}
	cases := []struct {
		name   string
		input  string
		anon   bool
		native bool
		want   string
	}{
		{"nonanonymized other group", "Hi @{g1/Alice Smith}", false, false, "Hi @g1/Alice Smith"},
		{"anonymized other group", "Hi @{g1/Alice Smith}", true, false, "Hi @g1/Rust Ocean Q7M5K"},
		{"native addressed group", "Hi @{g1/Alice Smith}", false, true, "Hi @native"},
		{"unknown stable handle", "Hi @{g1/P7}", true, false, "Hi @g1/P7"},
		{"unknown name suppressed on anon", "Hi @{g1/Bob Private}", true, false, "Hi @g1/unknown"},
		{"unknown name nonanon", "Hi @{g1/Bob Private}", false, false, "Hi @g1/Bob Private"},
		{"escaped reference", "Hi \\@{g1/Alice Smith}", true, false, "Hi \\@{g1/Alice Smith}"},
		{"unrelated native name", "Hi @Alice", true, false, "Hi @Alice"},
		{"literal reference does not tag", "Hi @g1/Alice Smith", false, true, "Hi @g1/Alice Smith"},
		{"repeated references", "@{g1/Alice Smith}, @{g1/Alice Smith}", true, false, "@g1/Rust Ocean Q7M5K, @g1/Rust Ocean Q7M5K"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notifications := 0
			got := FormatCrossReferences(tc.input, refs, tc.anon, func(ref CrossReference) (string, bool) {
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
		if got := FormatCrossReferences("Hi @{g1/P1}", []CrossReference{ref}, tc.anon, nil); got != "Hi @g1/"+tc.want {
			t.Fatalf("formatted alias with anonymized=%t: %q", tc.anon, got)
		}
	}
	ref.Pseudonym = ""
	if got := ReferenceDisplayLabel(ref, true); got != "P1" {
		t.Fatalf("missing pseudonym should fall back to stable handle, got %q", got)
	}
}
