package main

import (
	"context"
	"strings"
	"testing"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// The indecis engine masks what the CRF leaves readable, the indirect
// identifiers and the health data of the message, and the response gets them
// back. It needs the model in the default cache: it is skipped otherwise,
// rather than downloading 310 MB in a unit test run (the e2e suite covers the
// download).
func TestPreRequest_IndecisEngine_MasksIndirectIdentifiers(t *testing.T) {
	cfg := defaultConfig()
	cfg.Engine = engineIndecis
	if src, err := modelSourceFromConfig(cfg); testing.Short() || err != nil || !src.cached() {
		t.Skip("needs the indecis model in the default cache")
	}
	text := "Mon fils Lucas, 8 ans, est suivi au CMP de Villeurbanne pour un TDAH : comment l'aider à l'école ?"
	out, host := preRequestFR(t, `{"engine":"indecis","language":"fr"}`, text)

	if !out.Allowed || out.NoResponseRewrite {
		t.Fatalf("expected a rewritten request, got allowed=%v norewrite=%v", out.Allowed, out.NoResponseRewrite)
	}
	for _, clear := range []string{"Lucas", "8 ans", "Villeurbanne", "TDAH"} {
		if strings.Contains(out.ModifiedMessagesJson, clear) {
			t.Errorf("%q reached the LLM: %s", clear, out.ModifiedMessagesJson)
		}
	}
	if !strings.Contains(out.ModifiedMessagesJson, "⟦PERSON_") {
		t.Errorf("no PERSON placeholder in %s", out.ModifiedMessagesJson)
	}
	if evt := host.waitForEvent(t); evt.Type != "sensitive-data.detected" {
		t.Errorf("event type = %q, want sensitive-data.detected", evt.Type)
	}

	// The response quotes the placeholders the LLM read: they are restored.
	placeholders := placeholderPattern.FindAllString(out.ModifiedMessagesJson, -1)
	res, err := newPlugin().PostResponse(context.Background(), &proto.PostResponseInput{
		Ctx:             &proto.RequestContext{OrgId: "org-1", UserId: "user-1", NodeId: "node-1"},
		ResponseContent: strings.Join(placeholders, " "),
		NodeState:       out.NodeState,
	})
	if err != nil {
		t.Fatalf("PostResponse: %v", err)
	}
	for _, original := range []string{"Lucas", "TDAH"} {
		if !strings.Contains(res.ModifiedResponseContent, original) {
			t.Errorf("%q not restored in %q", original, res.ModifiedResponseContent)
		}
	}
}
