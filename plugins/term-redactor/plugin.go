package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// secretKeyAPIAuthValue is the key under which the external API's auth value
// is stored via SetSecret, scoped per (org, plugin, node) — never in the
// plain config JSON. Mirrors plugins/mcp-bridge's secretKeyAuthValue.
const secretKeyAPIAuthValue = "apiAuthValue"

// Plugin implémente proto.XoloPluginServer.
type Plugin struct {
	proto.UnimplementedXoloPluginServer

	cache *termCache

	hostMu     sync.Mutex
	hostClient pluginsdk.HostClient
}

func newPlugin() *Plugin {
	return &Plugin{cache: newTermCache(newHTTPTermFetcher())}
}

// SetHostClient implémente pluginsdk.HostClientSetter : le runtime l'appelle
// une fois la connexion au XoloHostService établie.
func (p *Plugin) SetHostClient(c pluginsdk.HostClient) {
	p.hostMu.Lock()
	defer p.hostMu.Unlock()
	p.hostClient = c
}

func (p *Plugin) getHostClient() pluginsdk.HostClient {
	p.hostMu.Lock()
	defer p.hostMu.Unlock()
	return p.hostClient
}

// emitEvent émet un événement de façon non bloquante (l'appel gRPC vers
// l'hôte ne doit pas ralentir le traitement de la requête).
func (p *Plugin) emitEvent(evt pluginsdk.Event) {
	hc := p.getHostClient()
	if hc == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := hc.EmitEvent(ctx, evt); err != nil {
			slog.Warn("term-redactor: could not emit event", slog.Any("error", err))
		}
	}()
}

// Motifs d'un passage en passe-plat, publiés dans l'événement associé.
const (
	passthroughConfigError      = "configuration invalide"
	passthroughRequestParse     = "corps de requête illisible"
	passthroughMessagesParse    = "messages illisibles"
	passthroughTermsUnavailable = "liste de termes indisponible"
	passthroughMarshalMessages  = "sérialisation des messages impossible"
)

// passthroughOnError laisse la requête passer sans masquage et le signale par
// un événement : un passe-plat silencieux ne laisserait aucune trace du fait
// que des noms de clients/patrimoine ont pu atteindre le modèle en clair.
func (p *Plugin) passthroughOnError(ctx context.Context, in *proto.PreRequestInput, reason string, err error) *proto.PreRequestOutput {
	slog.WarnContext(ctx, "term-redactor: "+reason+", passing through", slog.Any("error", err))
	attrs := map[string]string{"reason": reason}
	if err != nil {
		attrs["error"] = err.Error()
	}
	p.emitEvent(pluginsdk.Event{
		PluginName: "term-redactor",
		OrgID:      in.GetCtx().GetOrgId(),
		UserID:     in.GetCtx().GetUserId(),
		Type:       "passthrough",
		Severity:   "warning",
		Message:    "Requête transmise sans masquage : " + reason,
		Attributes: attrs,
	})
	return passthroughOutput()
}

// passthroughOutput lets the request through untouched. No state is stashed,
// so PostResponse has nothing to restore: the host is told it can stream the
// response live rather than buffer it.
func passthroughOutput() *proto.PreRequestOutput {
	return &proto.PreRequestOutput{Allowed: true, NoResponseRewrite: true}
}

func (p *Plugin) Describe(_ context.Context, _ *proto.DescribeRequest) (*proto.PluginDescriptor, error) {
	return &proto.PluginDescriptor{
		Name:        "term-redactor",
		Version:     "0.1.0",
		Description: "Masque les noms (clients, patrimoine) issus d'une source externe avant le LLM, et les rétablit dans la réponse.",
		Capabilities: []proto.PluginDescriptor_Capability{
			proto.PluginDescriptor_PRE_REQUEST,
			proto.PluginDescriptor_POST_RESPONSE,
		},
		InputPorts: []*proto.PortDescriptor{
			{Name: "request", PortType: "request", Required: true},
		},
		OutputPorts: []*proto.PortDescriptor{
			{Name: "request", PortType: "request"},
		},
		ConfigSchema: configSchemaJSON,
	}, nil
}

// pluginState is the opaque blob stored in node_state between PreRequest and
// PostResponse: the token->original-name mapping for terms actually found in
// this request. Simpler than pseudonymizer's own state — a direct exact-match
// lookup, no NER session bookkeeping needed.
type pluginState struct {
	Mapping map[string]string `json:"mapping"`
}

func (p *Plugin) PreRequest(ctx context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
	cfg, err := parseConfig(in.GetCtx().GetConfigJson())
	if err != nil {
		return p.passthroughOnError(ctx, in, passthroughConfigError, err), nil
	}

	// Resolve request body: prefer from input port, fall back to Model (full request body JSON).
	requestJSON := ""
	if in.InputsJson != "" {
		var inputs map[string]any
		if err := json.Unmarshal([]byte(in.InputsJson), &inputs); err == nil {
			if v, ok := inputs["request"].(string); ok {
				requestJSON = v
			}
		}
	}
	if requestJSON == "" {
		requestJSON = in.Model
	}

	var requestBody map[string]any
	if requestJSON != "" {
		if err := json.Unmarshal([]byte(requestJSON), &requestBody); err != nil {
			return p.passthroughOnError(ctx, in, passthroughRequestParse, err), nil
		}
	}

	messagesJSON := in.GetMessagesJson()
	if messagesJSON == "" && requestBody != nil {
		if msgs, ok := requestBody["messages"]; ok {
			if b, err := json.Marshal(msgs); err == nil {
				messagesJSON = string(b)
			}
		}
	}
	if messagesJSON == "" {
		return passthroughOutput(), nil
	}

	var messages []map[string]any
	if err := json.Unmarshal([]byte(messagesJSON), &messages); err != nil {
		return p.passthroughOnError(ctx, in, passthroughMessagesParse, err), nil
	}

	var authValue string
	if hc := p.getHostClient(); hc != nil {
		v, found, err := hc.GetSecret(ctx, in.GetCtx().GetOrgId(), "term-redactor", in.GetCtx().GetNodeId(), secretKeyAPIAuthValue)
		if err != nil {
			slog.WarnContext(ctx, "term-redactor: could not read API auth secret", slog.Any("error", err))
		} else if found {
			authValue = v
		}
	}

	entry, stale, err := p.cache.resolveTerms(ctx, in.GetCtx().GetOrgId(), in.GetCtx().GetNodeId(), cfg, authValue)
	if err != nil {
		// True fail-open path: no cache at all and the fetch just failed. The
		// conversation must never be blocked by this external dependency.
		return p.passthroughOnError(ctx, in, passthroughTermsUnavailable, err), nil
	}
	if entry == nil || len(entry.terms) == 0 {
		return passthroughOutput(), nil
	}
	if stale {
		slog.WarnContext(ctx, "term-redactor: serving stale term list, external API unreachable",
			slog.String("org_id", in.GetCtx().GetOrgId()),
		)
	}

	tokenFor := func(uuid string) string { return entry.tokenOf[uuid] }

	used := make(map[string]string)
	filtered := make([]map[string]any, 0, len(messages))
	for i, msg := range messages {
		content, ok := msg["content"]
		if !ok {
			filtered = append(filtered, messages[i])
			continue
		}
		switch c := content.(type) {
		case string:
			redacted, hits := entry.matcher.Redact(c, tokenFor)
			messages[i]["content"] = redacted
			for tok, name := range hits {
				used[tok] = name
			}
			filtered = append(filtered, messages[i])
		case []any:
			// Only text parts carry names we can match against; tool parts,
			// attachments and thinking blocks pass through unchanged (this
			// plugin does no attachment text extraction — pseudonymizer does
			// that in its own PreRequest, which runs after this node).
			kept := make([]any, 0, len(c))
			for _, part := range c {
				partMap, ok := part.(map[string]any)
				if !ok {
					kept = append(kept, part)
					continue
				}
				partType, _ := partMap["type"].(string)
				if partType != "text" {
					kept = append(kept, part)
					continue
				}
				text, _ := partMap["text"].(string)
				redacted, hits := entry.matcher.Redact(text, tokenFor)
				updated := make(map[string]any, len(partMap))
				for k, v := range partMap {
					updated[k] = v
				}
				updated["text"] = redacted
				kept = append(kept, updated)
				for tok, name := range hits {
					used[tok] = name
				}
			}
			messages[i]["content"] = kept
			filtered = append(filtered, messages[i])
		default:
			filtered = append(filtered, messages[i])
		}
	}

	modifiedMessagesJSON, err := json.Marshal(filtered)
	if err != nil {
		return p.passthroughOnError(ctx, in, passthroughMarshalMessages, err), nil
	}

	// Rebuild the full request body with redacted messages for the output port.
	var outputsJSON string
	if requestBody != nil {
		requestBody["messages"] = filtered
		if b, err := json.Marshal(requestBody); err == nil {
			outputs := map[string]any{"request": string(b)}
			if ob, err := json.Marshal(outputs); err == nil {
				outputsJSON = string(ob)
			}
		}
	}

	stateJSON, err := json.Marshal(pluginState{Mapping: used})
	if err != nil {
		slog.WarnContext(ctx, "term-redactor: failed to marshal state", slog.Any("error", err))
		stateJSON = []byte("{}")
	}

	if len(used) > 0 {
		attrs := map[string]string{
			"count": strconv.Itoa(len(used)),
			"stale": strconv.FormatBool(stale),
		}
		if entry.collisions > 0 {
			attrs["token_collisions"] = strconv.Itoa(entry.collisions)
		}
		p.emitEvent(pluginsdk.Event{
			PluginName: "term-redactor",
			OrgID:      in.GetCtx().GetOrgId(),
			UserID:     in.GetCtx().GetUserId(),
			Type:       "term-redactor.redacted",
			Severity:   "info",
			Message:    fmt.Sprintf("%d terme(s) masqué(s)", len(used)),
			Attributes: attrs,
		})
	}

	return &proto.PreRequestOutput{
		Allowed:              true,
		ModifiedMessagesJson: string(modifiedMessagesJSON),
		OutputsJson:          outputsJSON,
		NodeState:            stateJSON,
		// With nothing redacted, PostResponse has nothing to restore and
		// returns the response untouched — the host can stream it live.
		NoResponseRewrite: len(used) == 0,
	}, nil
}

func (p *Plugin) PostResponse(_ context.Context, in *proto.PostResponseInput) (*proto.PostResponseOutput, error) {
	if len(in.NodeState) == 0 {
		return &proto.PostResponseOutput{}, nil
	}

	var state pluginState
	if err := json.Unmarshal(in.NodeState, &state); err != nil {
		return &proto.PostResponseOutput{}, nil
	}

	restored := restoreTerms(in.ResponseContent, state.Mapping)
	return &proto.PostResponseOutput{ModifiedResponseContent: restored}, nil
}

// restoreTerms replaces every placeholder token in text with its original
// name — a direct reverse lookup, unlike pseudonymizer's deanonymize which
// resolves an NER session's own placeholders. Same shape otherwise (a plain
// strings.ReplaceAll loop over the mapping).
func restoreTerms(text string, mapping map[string]string) string {
	result := text
	for token, original := range mapping {
		result = strings.ReplaceAll(result, token, original)
	}
	return result
}
