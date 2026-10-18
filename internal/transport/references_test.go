package transport

import "testing"

func TestSenderAliasFormattingDoesNotConfuseARealNameWithItsAlias(t *testing.T) {
	for _, tc := range []struct {
		name      string
		anonymous bool
		want      string
	}{
		{"Alice", false, "Alice (Amber Badger)"},
		{"Amber Badger", false, "Amber Badger (Amber Badger)"},
		{"Alice", true, "Amber Badger"},
		{"", false, "Amber Badger"},
	} {
		got := (Sender{Alias: "Amber Badger", Anonymized: tc.anonymous}).NameWithAlias(tc.name)
		if got != tc.want {
			t.Fatalf("name=%q anonymous=%t got=%q want=%q", tc.name, tc.anonymous, got, tc.want)
		}
	}
}

func TestRewriteCrossReferencesEscapingAndAuthorization(t *testing.T) {
	refs := []CrossReference{{Token: "@{Amber Badger}", ActorID: "u_abcdefghij"}}
	render := func(ref CrossReference) (string, bool) {
		if ref.ActorID != "u_abcdefghij" {
			return "", false
		}
		return "@native", true
	}
	cases := []struct{ source, want string }{
		{"Hi @{Amber Badger}", "Hi @native"},
		{"Hi \\@{Amber Badger}", "Hi \\@{Amber Badger}"},
		{"Hi @{Azure Bear}", "Hi @{Azure Bear}"},
		{"Hi @{Amber Badger} and @{Amber Badger}", "Hi @native and @native"},
		{`\@{Amber Badger} @{Amber Badger}`, `\@{Amber Badger} @native`},
		{`\\@{Amber Badger}`, `\\@native`},
		{"Hi @{Amber Bear}", "Hi @{Amber Bear}"},
		{"@{unfinished Hi @{Amber Badger}", "@{unfinished Hi @{Amber Badger}"},
	}
	for _, tc := range cases {
		if got := RewriteCrossReferences(tc.source, refs, render); got != tc.want {
			t.Errorf("rewrite %q = %q, want %q", tc.source, got, tc.want)
		}
	}
}

func TestCrossReferencesPreservePostedTextUnlessNative(t *testing.T) {
	refs := []CrossReference{{Token: "@{Amber Badger}", ActorID: "u_abcdefghij", Name: "Alice Smith", Alias: "Amber Badger"}}
	cases := []struct {
		name   string
		input  string
		native bool
		want   string
	}{
		{"known reference fallback", "Hi @{Amber Badger}", false, "Hi @{Amber Badger}"},
		{"native addressed group", "Hi @{Amber Badger}", true, "Hi @native"},
		{"unknown stable handle", "Hi @{Azure Fox}", false, "Hi @{Azure Fox}"},
		{"unknown name", "Hi @{Azure Falcon}", false, "Hi @{Azure Falcon}"},
		{"escaped reference", "Hi \\@{Amber Badger}", false, "Hi \\@{Amber Badger}"},
		{"unrelated native name", "Hi @Alice", false, "Hi @Alice"},
		{"literal reference does not tag", "Hi @Amber Badger", true, "Hi @Amber Badger"},
		{"repeated references", "@{Amber Badger}, @{Amber Badger}", false, "@{Amber Badger}, @{Amber Badger}"},
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

func TestReferenceDisplayLabelAlwaysIncludesAlias(t *testing.T) {
	ref := CrossReference{
		Token: "@{Amber Bear}", ActorID: "u_abcdefghij",
		Name: "Alice Smith", Alias: "Amber Badger",
	}
	for _, tc := range []struct {
		anon bool
		want string
	}{
		{true, "Amber Badger"},
		{false, "Alice Smith (Amber Badger)"},
	} {
		if got := ReferenceDisplayLabel(ref, tc.anon); got != tc.want {
			t.Fatalf("alias with anonymized=%t: got %q want %q", tc.anon, got, tc.want)
		}
		if got := RewriteCrossReferences("Hi @{Amber Bear}", []CrossReference{ref}, nil); got != "Hi @{Amber Bear}" {
			t.Fatalf("formatted alias with anonymized=%t: %q", tc.anon, got)
		}
	}
	ref.Name = ""
	if got := ReferenceDisplayLabel(ref, true); got != "Amber Badger" {
		t.Fatalf("missing display name should retain alias, got %q", got)
	}
}
