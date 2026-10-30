package authn

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fixedAuthenticator struct{ user *User }

func (a fixedAuthenticator) Authenticate(http.ResponseWriter, *http.Request) (*User, error) {
	return a.user, nil
}

type failingAuthenticator struct{}

func (failingAuthenticator) Authenticate(http.ResponseWriter, *http.Request) (*User, error) {
	return nil, errors.New("token expired")
}

func TestOptionalLetsSignedOutRequestsThrough(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first Authenticator
		user  *User
		want  string
	}{
		{"signed in", fixedAuthenticator{nil}, &User{Provider: "oidc", Subject: "jane"}, "signed in jane"},
		{"signed out", fixedAuthenticator{nil}, nil, "signed out"},
		{"unusable credential, signed out", failingAuthenticator{}, nil, "signed out"},
		{"unusable credential, signed in otherwise", failingAuthenticator{}, &User{Provider: "oidc", Subject: "jane"}, "signed in jane"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			signedOut := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = "signed out"
			})
			signedIn := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = "signed in " + ContextUser(r.Context()).Subject
			})
			Optional(signedOut, tc.first, fixedAuthenticator{tc.user})(signedIn).
				ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/locale", nil))
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
