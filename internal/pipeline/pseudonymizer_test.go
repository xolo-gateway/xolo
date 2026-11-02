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

// pseudoState mirrors the JSON shape persisted as NodeState by the real
// plugins/pseudonymizer plugin: {"mapping": {"[EMAIL_1]": "jean.dupont@example.com", ...}}.
type pseudoState struct {
	Mapping map[string]string `json:"mapping"`
}

// TestPipeline_PseudonymizerAnonymizationRoundTrip verifies that data is
// anonymized before it reaches the LLM and restored once the response comes
// back, using the real go-anon anonymize/deanonymize logic (regex-only
// detection, no NER model required).
func TestPipeline_PseudonymizerAnonymizationRoundTrip(t *testing.T) {
	const userMessage = "Contactez-moi à jean.dupont@example.com pour plus d'infos."

	// precompute the placeholder so the fake LLM response can reuse it. Since
	// each Anonymize call generates a new nonce per session, we reuse the same
	// probe session both for the probe and for the real Anonymize call below,
	// so the placeholder is byte-identical between the two.
	probeSession := anonymizer.NewSession()
	if _, err := pipelinetest.NewRegexAnonymizer().Anonymize(context.Background(), userMessage, anonymizer.WithSession(probeSession)); err != nil {
		t.Fatalf("probe anonymize failed: %v", err)
	}
	var placeholder string
	for ph := range probeSession.Mapping {
		placeholder = ph
		break
	}
	if placeholder == "" {
		t.Fatal("expected the probe anonymization to detect the email address")
	}

	pseudo := &pipelinetest.PluginClient{
		PreRequestFunc: func(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
			var messages []map[string]any
			if err := json.Unmarshal([]byte(in.GetMessagesJson()), &messages); err != nil {
				return nil, err
			}

			anon := pipelinetest.NewRegexAnonymizer()
			// Reuse the probe session so its nonce carries over, making the
			// placeholder byte-identical between probe and the real run.
			session := probeSession

			for _, msg := range messages {
				content, ok := msg["content"].(string)
				if !ok {
					continue
				}
				result, err := anon.Anonymize(context.Background(), content, anonymizer.WithSession(session))
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
		Register("pseudonymizer", pipelinetest.PrePostDescriptor("pseudonymizer"), pseudo)

	llmResponse := fmt.Sprintf("Bien sûr, je recontacterai %s rapidement.", placeholder)
	resolver := pipelinetest.NewModelResolver().
		WithResponse("org/gpt4", llmResponse)

	graph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("pseudo", "pseudonymizer").
		ModelWithProxy("mdl", "org/gpt4").
		Sink("sink").
		Edge("gen", "request", "pseudo", "request").
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

	// The LLM must never see the raw email address, only its placeholder.
	if strings.Contains(result.Forward.FinalMessagesJSON, "jean.dupont@example.com") {
		t.Errorf("FinalMessagesJSON leaks raw PII: %q", result.Forward.FinalMessagesJSON)
	}
	if !strings.Contains(result.Forward.FinalMessagesJSON, placeholder) {
		t.Errorf("FinalMessagesJSON = %q, want it to contain placeholder %q", result.Forward.FinalMessagesJSON, placeholder)
	}

	// The final response must have the original value restored, with no
	// leftover placeholder.
	if !strings.Contains(result.FinalContent, "jean.dupont@example.com") {
		t.Errorf("FinalContent = %q, want it to contain the original email", result.FinalContent)
	}
	if strings.Contains(result.FinalContent, placeholder) {
		t.Errorf("FinalContent = %q, still contains placeholder %q", result.FinalContent, placeholder)
	}
}
