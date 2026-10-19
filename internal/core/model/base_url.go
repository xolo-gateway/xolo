package model

import "net/url"

// IsValidBaseURL reports whether s can serve as a provider or model base URL:
// an absolute http(s) URL without credentials, query or fragment. The URL is
// stored in clear, and the provider clients append their paths to it.
func IsValidBaseURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http") &&
		u.User == nil && u.RawQuery == "" && u.Fragment == ""
}
