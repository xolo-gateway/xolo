package pipeline

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/pkg/errors"

	genaiProxy "github.com/bornholm/genai/proxy"
)

// TestRejectionError_AnswersAsARefusal checks that a refusal reaching the
// proxy as the error of the model call, such as a tool result rejected inside
// the tool loop, is answered with a 403 that names its reason, wrapped or not.
func TestRejectionError_AnswersAsARefusal(t *testing.T) {
	for name, err := range map[string]error{
		"bare":    &RejectionError{Reason: "contenu hostile"},
		"wrapped": fmt.Errorf("chat completion: %w", &RejectionError{Reason: "contenu hostile"}),
	} {
		t.Run(name, func(t *testing.T) {
			var apiErr *genaiProxy.APIError
			if !errors.As(err, &apiErr) {
				t.Fatal("a rejection does not read as an API error")
			}
			if apiErr.StatusCode != http.StatusForbidden || apiErr.Type != "permission_error" {
				t.Errorf("API error = %+v, want a 403 permission_error", apiErr)
			}
			if apiErr.Message != "request rejected by pipeline: contenu hostile" {
				t.Errorf("message = %q", apiErr.Message)
			}

			var rejection *RejectionError
			if !errors.As(err, &rejection) || rejection.Reason != "contenu hostile" {
				t.Errorf("the rejection itself is no longer found: %v", rejection)
			}
		})
	}
}
