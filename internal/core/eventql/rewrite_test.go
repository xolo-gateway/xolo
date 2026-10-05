package eventql

import (
	"strings"
	"testing"
)

func TestRewriteIdentifiers(t *testing.T) {
	mapping := map[string]map[string]string{"user": {"legacy": "new-user"}, "org": {"old-org": "new-org"}, "actor_id": {"legacy": "new-user"}}
	for _, tc := range []struct{ input, want string }{
		{`{user="legacy",org!="old-org"} | actor_id="legacy"`, `{user="new-user",org!="new-org"} | actor_id="new-user"`},
		{`{ user = "legacy" } |= "legacy" | email="legacy"`, `{ user = "new-user" } |= "legacy" | email="legacy"`},
		{`{} |= "échec" | actor_id="legacy"`, `{} |= "échec" | actor_id="new-user"`},
		{`{user="legacy-suffix"}`, `{user="legacy-suffix"}`},
		{`{user=~".*"}`, `{user=~".*"}`},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := RewriteIdentifiers(tc.input, mapping)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	_, err := RewriteIdentifiers(`{user=~"legacy|another"}`, mapping)
	if err == nil || !strings.Contains(err.Error(), "serialized override") {
		t.Fatalf("expected actionable regex diagnostic, got %v", err)
	}
}
