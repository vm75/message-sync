package verification

import (
	"net/url"
	"strings"
)

var freeMailDomains = map[string]bool{"gmail.com": true, "googlemail.com": true, "yahoo.com": true, "hotmail.com": true, "outlook.com": true, "icloud.com": true, "proton.me": true, "protonmail.com": true}

type Advisory struct {
	DomainClass     string
	LinkedInValid   bool
	EvidencePresent bool
	Result          string
}

func Assess(email, linkedin string, evidencePresent bool) Advisory {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(email)), "@")
	domain := ""
	if len(parts) == 2 {
		domain = parts[1]
	}
	class := "WORK"
	if freeMailDomains[domain] {
		class = "PERSONAL_OR_FREE"
	}
	linked := false
	if linkedin != "" {
		u, err := url.Parse(strings.TrimSpace(linkedin))
		linked = err == nil && u.Scheme == "https" && (strings.EqualFold(u.Host, "linkedin.com") || strings.HasSuffix(strings.ToLower(u.Host), ".linkedin.com"))
	}
	result := "UNVERIFIED"
	if class == "WORK" && linked && evidencePresent {
		result = "MEDIUM"
	} else if class == "PERSONAL_OR_FREE" {
		result = "LOW"
	}
	return Advisory{DomainClass: class, LinkedInValid: linked, EvidencePresent: evidencePresent, Result: result}
}
