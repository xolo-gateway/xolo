package model

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// SlugPattern matches a DNS-label-like identifier, the form external systems
// (Kubernetes operators, Terraform providers) can always produce. It lives here
// rather than in the provisioning service because the same shape is required in
// three unrelated places: organization slugs, tenant slugs, and the tenant
// segment extracted from a request host in multi-tenant mode — a subdomain
// label is exactly a DNS label.
var SlugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// MaxSlugLength is the maximum length of a slug, aligned on the DNS label limit.
const MaxSlugLength = 63

// IsValidSlug reports whether s is a well-formed slug.
func IsValidSlug(s string) bool {
	return s != "" && len(s) <= MaxSlugLength && SlugPattern.MatchString(s)
}

// Slugify derives a slug from a free-form name: accents are dropped, runs of
// other characters become a single dash. It returns fallback when nothing is
// left.
func Slugify(name, fallback string) string {
	decomposed := norm.NFKD.String(strings.ToLower(name))
	var b strings.Builder
	dash := false
	for _, r := range decomposed {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
	}
	slug := b.String()
	if len(slug) > MaxSlugLength {
		slug = strings.TrimRight(slug[:MaxSlugLength], "-")
	}
	if !IsValidSlug(slug) {
		return fallback
	}
	return slug
}
