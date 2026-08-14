package model

import "regexp"

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
