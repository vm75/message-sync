package commands

import (
	"strings"
	"testing"
)

func TestBuiltinsSupplementEditableSettings(t *testing.T) {
	for _, cfg := range []string{"", "!custom", "/local"} {
		for _, text := range []string{"/local", "/local keep here", "  /local\nprivate"} {
			if !MatchLocal(text, cfg) {
				t.Fatalf("built-in disabled: text=%q config=%q", text, cfg)
			}
		}
	}
	if MatchLocal("/locality is ordinary", "") {
		t.Fatal("built-in swallowed another command")
	}
	if !MatchLocal("!customhello", "!custom") {
		t.Fatal("changed configured prefix matching")
	}
	for _, cfg := range []string{"", "/votes", "/summary"} {
		if !MatchSummary(" /summary ", cfg) {
			t.Fatalf("built-in summary disabled by %q", cfg)
		}
		if MatchSummary("/summary extra", cfg) {
			t.Fatal("summary matched partial text")
		}
	}
	if !MatchSummary("aggregate-response", "") || !MatchSummary("/votes", "/votes") {
		t.Fatal("configured/default trigger lost")
	}
	if MatchSummary("aggregate-response", "/votes") {
		t.Fatal("replaced editable default still matched")
	}
}

func TestHelpListsAllCommandsAndDistinctConfiguredValues(t *testing.T) {
	text := Describe(Settings{LocalPrefixes: "!local /local !local", AggregationTriggers: "/votes /summary /votes"}, Settings{})
	for _, want := range []string{"/help", "/local", "/summary", "/dm", "/start", "!local", "/votes", "aggregate-response", "reply to a synchronized poll"} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(want)) {
			t.Fatalf("missing %q in help", want)
		}
	}
	if strings.Count(text, "!local") != 1 || strings.Count(text, "/votes") != 1 {
		t.Fatal("duplicate configured commands in help")
	}
}
