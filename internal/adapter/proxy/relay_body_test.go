package proxy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/xolo-gateway/xolo/internal/pipeline"
)

func decodeBody(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("invalid body %s: %v", body, err)
	}
	return decoded
}

func TestWithPipelineMessages_HoistsInjectedSystemAfterTheClients(t *testing.T) {
	body := `{"model":"m","system":[{"type":"text","text":"attribution"},{"type":"text","text":"client <rules>","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[{"role":"user","content":"Hi Jean <3"}],"safeguards":[{"type":"dangerous_tool_use"}]}`
	original := `[{"role":"user","content":"Hi Jean <3"}]`
	final := `[{"role":"system","content":"Keep <PERSON_1> as is."},{"role":"user","content":"Hi PERSON_1 <3"}]`

	out, err := withPipelineMessages([]byte(body), original, final)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeBody(t, out)

	wantSystem := `[{"type":"text","text":"attribution"},{"type":"text","text":"client <rules>","cache_control":{"type":"ephemeral"}},{"type":"text","text":"Keep <PERSON_1> as is."}]`
	if string(got["system"]) != wantSystem {
		t.Errorf("system = %s", got["system"])
	}
	if string(got["messages"]) != `[{"role":"user","content":"Hi PERSON_1 <3"}]` {
		t.Errorf("messages = %s", got["messages"])
	}
	if string(got["safeguards"]) != `[{"type":"dangerous_tool_use"}]` {
		t.Errorf("safeguards = %s", got["safeguards"])
	}
	if strings.Contains(string(out), "\\u003c") {
		t.Errorf("body was HTML-escaped: %s", out)
	}
}

func TestWithPipelineMessages_StringOrMissingClientSystem(t *testing.T) {
	final := `[{"role":"system","content":"injected"},{"role":"user","content":"hi"}]`
	for name, tc := range map[string]struct{ body, want string }{
		"string":  {`{"system":"client","messages":[]}`, `[{"type":"text","text":"client"},{"type":"text","text":"injected"}]`},
		"missing": {`{"messages":[]}`, `[{"type":"text","text":"injected"}]`},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := withPipelineMessages([]byte(tc.body), `[{"role":"user","content":"hi"}]`, final)
			if err != nil {
				t.Fatal(err)
			}
			if got := decodeBody(t, out)["system"]; string(got) != tc.want {
				t.Errorf("system = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestWithPipelineMessages_ClientLeadingSystemStaysInPlace(t *testing.T) {
	body := `{"system":"client","messages":[{"role":"system","content":"mid"},{"role":"user","content":"hi"}]}`
	final := `[{"role":"system","content":"mid, rewritten"},{"role":"user","content":"hi"}]`

	out, err := withPipelineMessages([]byte(body), `[{"role":"system","content":"mid"},{"role":"user","content":"hi"}]`, final)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeBody(t, out)
	if string(got["system"]) != `"client"` || string(got["messages"]) != final {
		t.Errorf("system = %s, messages = %s", got["system"], got["messages"])
	}
}

func TestWithPipelineMessages_RefusesAnInjectedSystemItCannotHoist(t *testing.T) {
	final := `[{"role":"system","content":{"unexpected":"object"}},{"role":"user","content":"hi"}]`
	if _, err := withPipelineMessages([]byte(`{"messages":[]}`), `[{"role":"user","content":"hi"}]`, final); err == nil {
		t.Error("a system message that cannot be hoisted must fail, not be relayed in place")
	}
}

// A Messages request whose body cannot carry the rewritten conversation must
// be refused: relayed, it would go upstream without the pipeline's rewrite.
func TestApplyModifiedMessages_FailsWhenTheBodyCannotCarryTheRewrite(t *testing.T) {
	ec := pipeline.ExecutionContext{MessagesJSON: `[{"role":"user","content":"Jean"}]`}
	forwardExec := &pipeline.ForwardExecution{FinalMessagesJSON: `[{"role":"user","content":"PERSON_1"}]`}
	req := &genaiProxy.ProxyRequest{Type: genaiProxy.RequestTypeMessage, Body: []byte(`not json`)}

	if err := applyModifiedMessages(context.Background(), req, ec, forwardExec); err == nil {
		t.Error("expected an error")
	}
}
