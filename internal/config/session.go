package config

import "github.com/pkg/errors"

// Validate refuses a session lifetime the OIDC session registry can not
// honour: a session is registered for exactly that long, so a cookie
// lasting as long as the browser has no registered counterpart.
func (s Session) Validate() error {
	if s.Cookie.MaxAge <= 0 {
		return errors.Errorf("XOLO_HTTP_SESSION_COOKIE_MAX_AGE must be positive, got %s: OIDC sessions are registered for that long", s.Cookie.MaxAge)
	}
	return nil
}
