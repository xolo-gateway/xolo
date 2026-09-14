package pipeline_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/bornholm/go-anon/pkg/anonymizer"
	"github.com/xolo-gateway/xolo/internal/pipeline/pipelinetest"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// termState mirrors the JSON shape persisted as NodeState by the real
// plugins/term-redactor plugin: {"mapping": {"[CLIENT_a1b2c3d4]": "Jean Dupont"}}.
type termState struct {
	Mapping map[string]string `json:"mapping"`
}

// TestPipeline_TermRedactorRunsBeforePseudonymizer verifies that a
// term-redactor node placed before pseudonymizer in the graph (a) redacts its
// own listed name before pseudonymizer ever sees the text, so pseudonymizer's
// own NER pass never has a chance to re-flag or corrupt that placeholder, and
// (b) on the way back, both plugins restore their own mapping without
// clobbering the other's, in the correct (reverse-topological) order.
func TestPipeline_TermRedactorRunsBeforePseudonymizer(t *testing.T) {
	const (
		userMessage = "Contactez Jean Dupont à jean.dupont@example.com pour plus d'infos."
		clientToken = "[CLIENT_a1b2c3d4]"
		clientName  = "Jean Dupont"
	)

	// Precompute the email placeholder the same way the pseudonymizer fake
	// below will, so the forged LLM response can reuse it verbatim.
	probeSession := anonymizer.NewSession()
	if _, err := pipelinetest.NewRegexAnonymizer().Anonymize(userMessage, anonymizer.WithSession(probeSession)); err != nil {
		t.Fatalf("probe anonymize failed: %v", err)
	}
	var emailPlaceholder string
	for ph := range probeSession.Mapping {
		emailPlaceholder = ph
		break
	}
	if emailPlaceholder == "" {
		t.Fatal("expected the probe anonymization to detect the email address")
	}

	// textSeenByPseudonymizer captures exactly what the downstream plugin
	// receives, so the test can assert term-redactor already ran on it.
	var textSeenByPseudonymizer string

	termRedactor := &pipelinetest.PluginClient{
		PreRequestFunc: func(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
			var messages []map[string]any
			if err := json.Unmarshal([]byte(in.GetMessagesJson()), &messages); err != nil {
				return nil, err
			}

			mapping := map[string]string{}
			for _, msg := range messages {
				content, ok := msg["content"].(string)
				if !ok {
					continue
				}
				if strings.Contains(content, clientName) {
					content = strings.ReplaceAll(content, clientName, clientToken)
					mapping[clientToken] = clientName
					msg["content"] = content
				}
			}

			modifiedMessagesJSON, err := json.Marshal(messages)
			if err != nil {
				return nil, err
			}
			nodeState, err := json.Marshal(termState{Mapping: mapping})
			if err != nil {
				return nil, err
			}

			return &proto.PreRequestOutput{
				Allowed:              true,
				ModifiedMessagesJson: string(modifiedMessagesJSON),
				NodeState:            nodeState,
			}, nil
		},
		PostResponseFunc: func(_ context.Context, in *proto.PostResponseInput) (*proto.PostResponseOutput, error) {
			var state termState
			if err := json.Unmarshal(in.GetNodeState(), &state); err != nil {
				return nil, err
			}
			content := in.GetResponseContent()
			for token, original := range state.Mapping {
				content = strings.ReplaceAll(content, token, original)
			}
			return &proto.PostResponseOutput{ModifiedResponseContent: content}, nil
		},
	}

	pseudo := &pipelinetest.PluginClient{
		PreRequestFunc: func(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
			var messages []map[string]any
			if err := json.Unmarshal([]byte(in.GetMessagesJson()), &messages); err != nil {
				return nil, err
			}

			anon := pipelinetest.NewRegexAnonymizer()
			session := probeSession // reuse the nonce so the placeholder matches

			for _, msg := range messages {
				content, ok := msg["content"].(string)
				if !ok {
					continue
				}
				textSeenByPseudonymizer = content // capture exactly what this node received
				result, err := anon.Anonymize(content, anonymizer.WithSession(session))
				if err != nil {
					return nil, err
				}
				msg["content"] = result.Text
			}

			modifiedMessagesJSON, err := json.Marshal(messages)
			if err != nil {
				return nil, err
			}
			nodeState, err := json.Marshal(pseudoState{Mapping: session.Mapping})
			if err != nil {
				return nil, err
			}

			return &proto.PreRequestOutput{
				Allowed:              true,
				ModifiedMessagesJson: string(modifiedMessagesJSON),
				NodeState:            nodeState,
			}, nil
		},
		PostResponseFunc: func(_ context.Context, in *proto.PostResponseInput) (*proto.PostResponseOutput, error) {
			var state pseudoState
			if err := json.Unmarshal(in.GetNodeState(), &state); err != nil {
				return nil, err
			}
			content := in.GetResponseContent()
			for placeholder, original := range state.Mapping {
				content = strings.ReplaceAll(content, placeholder, original)
			}
			return &proto.PostResponseOutput{ModifiedResponseContent: content}, nil
		},
	}

	plugins := pipelinetest.NewPluginProvider().
		Register("term-redactor", pipelinetest.PrePostDescriptor("term-redactor"), termRedactor).
		Register("pseudonymizer", pipelinetest.PrePostDescriptor("pseudonymizer"), pseudo)

	llmResponse := fmt.Sprintf("Bien sûr, je recontacterai %s via %s rapidement.", clientToken, emailPlaceholder)
	resolver := pipelinetest.NewModelResolver().
		WithResponse("org/gpt4", llmResponse)

	graph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("tr", "term-redactor").
		Plugin("pseudo", "pseudonymizer").
		ModelWithProxy("mdl", "org/gpt4").
		Sink("sink").
		Edge("gen", "request", "tr", "request").
		Edge("tr", "request", "pseudo", "request").
		Edge("pseudo", "request", "mdl", "request").
		Edge("mdl", "response", "sink", "response").
		Build()

	h := pipelinetest.New(
		pipelinetest.WithPlugins(plugins),
		pipelinetest.WithModelResolver(resolver),
	)

	ec := pipelinetest.NewExecutionContext(
		pipelinetest.WithMessagesJSON(fmt.Sprintf(`[{"role":"user","content":%q}]`, userMessage)),
	)

	result, err := h.Run(context.Background(), graph, ec)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result.Rejected {
		t.Fatalf("unexpected rejection: %s", result.RejectionReason)
	}

	// Forward order: pseudonymizer must only ever see term-redactor's already
	// -redacted text, never the raw client name.
	if strings.Contains(textSeenByPseudonymizer, clientName) {
		t.Errorf("pseudonymizer saw the raw client name: %q", textSeenByPseudonymizer)
	}
	if !strings.Contains(textSeenByPseudonymizer, clientToken) {
		t.Errorf("pseudonymizer text = %q, want it to already contain %q", textSeenByPseudonymizer, clientToken)
	}

	// The model itself must never see either the raw name or the raw email.
	if strings.Contains(result.Forward.FinalMessagesJSON, clientName) {
		t.Errorf("FinalMessagesJSON leaks the raw client name: %q", result.Forward.FinalMessagesJSON)
	}
	if strings.Contains(result.Forward.FinalMessagesJSON, "jean.dupont@example.com") {
		t.Errorf("FinalMessagesJSON leaks the raw email: %q", result.Forward.FinalMessagesJSON)
	}

	// Backward order: both placeholders must be resolved back to their
	// original value in the final response, with nothing left over.
	if !strings.Contains(result.FinalContent, clientName) {
		t.Errorf("FinalContent = %q, want the client name restored", result.FinalContent)
	}
	if !strings.Contains(result.FinalContent, "jean.dupont@example.com") {
		t.Errorf("FinalContent = %q, want the email restored", result.FinalContent)
	}
	if strings.Contains(result.FinalContent, clientToken) || strings.Contains(result.FinalContent, emailPlaceholder) {
		t.Errorf("FinalContent = %q, still contains a placeholder", result.FinalContent)
	}
}
