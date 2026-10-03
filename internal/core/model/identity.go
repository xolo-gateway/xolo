package model

import (
	"net/url"
	"strings"
	"unicode/utf8"
)

// Valid preserves exact bytes. Only declared identities require this public
// HTTPS profile; legacy provider names remain separate authentication metadata.
func (i Identity) Valid() bool {
	text := func(s string, max int) bool {
		if !utf8.ValidString(s) || len(s) == 0 || len(s) > max {
			return false
		}
		for _, r := range s {
			if r < 32 || r == 127 {
				return false
			}
		}
		return true
	}
	u, err := url.Parse(i.Issuer)
	return err == nil && text(i.Issuer, 2048) && strings.TrimSpace(i.Issuer) == i.Issuer && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && !strings.ContainsAny(i.Issuer, "?#") && text(i.Subject, 255)
}
