// Package commands defines the built-in conversation commands independently of
// editable sync-set prefixes and triggers.
package commands

import (
	"sort"
	"strings"
)

const Local = "/local"
const Summary = "/summary"

const Help = "message-sync commands\n/help — show all commands\n/local <message> — keep a group message and its later changes in this conversation only\n/summary — reply to a synchronized poll for live aggregated results\n/dm @{Amber Badger} <message> — send a private message in a private chat with the sync account/bot (requires both relay switches and verified shared membership)\n/start — Telegram private-chat help and bot reachability\nUse native Reply on a relayed private message to respond."

type Settings struct {
	LocalPrefixes       string
	AggregationTriggers string
}

func MatchLocal(text, configured string) bool {
	fields := strings.Fields(text)
	if len(fields) > 0 && fields[0] == Local {
		return true
	}
	for _, prefix := range strings.Fields(configured) {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

func MatchSummary(text, configured string) bool {
	text = strings.TrimSpace(text)
	if text == Summary {
		return true
	}
	if strings.TrimSpace(configured) == "" {
		configured = "aggregate-response"
	}
	for _, trigger := range strings.Fields(configured) {
		if text == trigger {
			return true
		}
	}
	return false
}

// Describe lists extra settings without set names or participant identities.
func Describe(settings ...Settings) string {
	local, triggers := map[string]bool{}, map[string]bool{}
	for _, s := range settings {
		for _, v := range strings.Fields(s.LocalPrefixes) {
			if v != Local {
				local[v] = true
			}
		}
		values := strings.Fields(s.AggregationTriggers)
		if len(values) == 0 {
			values = []string{"aggregate-response"}
		}
		for _, v := range values {
			if v != Summary {
				triggers[v] = true
			}
		}
	}
	text := Help
	for _, group := range []struct {
		label  string
		values map[string]bool
	}{{"Additional local-only prefixes (groups)", local}, {"Additional poll summary triggers (reply to a poll)", triggers}} {
		if len(group.values) == 0 {
			continue
		}
		values := make([]string, 0, len(group.values))
		for v := range group.values {
			values = append(values, v)
		}
		sort.Strings(values)
		text += "\n" + group.label + ": " + strings.Join(values, ", ")
	}
	return text
}
