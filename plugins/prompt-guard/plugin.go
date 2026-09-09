package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
	"github.com/xolo-gateway/xolo/plugins/internal/promptguard"
	"github.com/xolo-gateway/xolo/plugins/internal/requesttext"
)

const (
	PluginName    = "prompt-guard"
	PluginVersion = "0.1.0"
)

// Plugin scores each request for prompt injection without calling a model.
// It exposes the risk and the per-category scores as ports and leaves the
// decision to the pipeline, except for the optional hard block above a
// configured threshold.
type Plugin struct {
	proto.UnimplementedXoloPluginServer

	hostMu     sync.Mutex
	hostClient pluginsdk.HostClient

	// guards caches one Guard per distinct extra_rules text. Compiling a
	// rule set costs milliseconds; doing it on every request would not.
	guardsMu sync.Mutex
	guards   map[string]*promptguard.Guard
}

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

func (p *Plugin) Describe(_ context.Context, _ *proto.DescribeRequest) (*proto.PluginDescriptor, error) {
	outputs := []*proto.PortDescriptor{
		{Name: "request", PortType: "request", Required: true},
		{Name: "risk", PortType: "number"},
		{Name: "categories", PortType: "string"},
	}
	for _, c := range promptguard.Categories {
		outputs = append(outputs, &proto.PortDescriptor{Name: string(c), PortType: "number"})
	}
	outputs = append(outputs,
		&proto.PortDescriptor{Name: "top_rule", PortType: "string"},
		&proto.PortDescriptor{Name: "segment", PortType: "string"},
		&proto.PortDescriptor{Name: "suspicious", PortType: "boolean"},
		&proto.PortDescriptor{Name: "quoted", PortType: "boolean"},
		&proto.PortDescriptor{Name: "model_probability", PortType: "number"},
	)
	return &proto.PluginDescriptor{
		Name:         PluginName,
		Version:      PluginVersion,
		Description:  "Détecte les tentatives d'injection de prompt (remplacement des instructions, fuite du prompt système, détournement de rôle, obfuscation, abus d'outils, exfiltration) par règles, signaux structurels et un modèle statistique embarqué, sans appel à un LLM. Expose un risque dans [0, 1] et un score par catégorie ; bloque au-delà d'un seuil optionnel.",
		Capabilities: []proto.PluginDescriptor_Capability{proto.PluginDescriptor_PRE_REQUEST, proto.PluginDescriptor_TOOL_RESULT_INSPECTOR},
		InputPorts: []*proto.PortDescriptor{
			{Name: "request", PortType: "request", Required: true},
		},
		OutputPorts:  outputs,
		ConfigSchema: configSchemaJSON,
	}, nil
}

func (p *Plugin) PreRequest(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
	cfg := parseConfig(in.GetCtx().GetConfigJson())

	guard, err := p.guardFor(cfg)
	if err != nil {
		// A broken extra rule must not open the gate silently, nor close it
		// for everyone: run the default rules and say so.
		slog.Warn("prompt-guard: invalid extra_rules, using default rules only", slog.Any("error", err))
		guard = p.defaultGuard()
	}

	segments := []promptguard.Segment{
		{Kind: promptguard.SegmentUser, Text: requesttext.LastUserTurn(in.MessagesJson)},
	}
	if cfg.AnalyzeHistory {
		for _, t := range requesttext.EarlierUserTurns(in.MessagesJson) {
			segments = append(segments, promptguard.Segment{Kind: promptguard.SegmentHistory, Text: t})
		}
	}
	if cfg.AnalyzeToolResults {
		for _, t := range requesttext.ToolResults(in.MessagesJson) {
			segments = append(segments, promptguard.Segment{Kind: promptguard.SegmentTool, Text: t})
		}
	}

	a := guard.Assess(segments)

	if cfg.EventAbove > 0 && a.Risk >= cfg.EventAbove {
		p.emit(in.GetCtx(), a, cfg.BlockAbove > 0 && a.Risk >= cfg.BlockAbove)
	}

	if cfg.BlockAbove > 0 && a.Risk >= cfg.BlockAbove {
		return &proto.PreRequestOutput{
			Allowed:         false,
			RejectionReason: cfg.BlockMessage,
		}, nil
	}

	outputs := map[string]interface{}{
		"risk":              round3(a.Risk),
		"categories":        joinCategories(a),
		"top_rule":          a.TopRule,
		"segment":           string(a.Segment),
		"suspicious":        cfg.SuspiciousAbove > 0 && a.Risk >= cfg.SuspiciousAbove,
		"quoted":            a.Quoted,
		"model_probability": round3(a.ModelProbability),
	}
	for _, c := range promptguard.Categories {
		outputs[string(c)] = round3(a.Categories[c])
	}
	b, _ := json.Marshal(outputs)
	return &proto.PreRequestOutput{Allowed: true, OutputsJson: string(b)}, nil
}

// InspectToolResult scores a single tool result the gateway fetched itself
// (typically an MCP server's output) as a tool segment, before it reaches the
// model. This is the indirect-injection surface PreRequest never sees. It
// emits its own security.prompt_injection event above event_above and asks
// the gateway to block above block_above, exactly like PreRequest, so the
// same node config governs both entry points.
func (p *Plugin) InspectToolResult(_ context.Context, in *proto.InspectToolResultInput) (*proto.InspectToolResultOutput, error) {
	cfg := parseConfig(in.GetCtx().GetConfigJson())
	if !cfg.AnalyzeToolResults {
		return &proto.InspectToolResultOutput{}, nil
	}
	guard, err := p.guardFor(cfg)
	if err != nil {
		guard = p.defaultGuard()
	}
	a := guard.Assess([]promptguard.Segment{{Kind: promptguard.SegmentTool, Text: in.GetContent()}})
	blocked := cfg.BlockAbove > 0 && a.Risk >= cfg.BlockAbove
	if cfg.EventAbove > 0 && a.Risk >= cfg.EventAbove {
		p.emitToolResult(in.GetCtx(), in.GetToolName(), a, blocked)
	}
	reason := ""
	if blocked {
		reason = cfg.BlockMessage
	}
	return &proto.InspectToolResultOutput{Blocked: blocked, Reason: reason, Risk: round3(a.Risk)}, nil
}

// emitToolResult records the security event for a suspicious tool result. The
// tool name is safe to log; the result text is not, so it never leaves.
func (p *Plugin) emitToolResult(reqCtx *proto.RequestContext, toolName string, a promptguard.Assessment, blocked bool) {
	hc := p.getHostClient()
	if hc == nil {
		return
	}
	severity := "warning"
	verb := "suspect"
	if blocked {
		severity = "error"
		verb = "bloqué"
	}
	attrs := map[string]string{
		"risk":       fmt.Sprintf("%.3f", a.Risk),
		"categories": joinCategories(a),
		"top_rule":   a.TopRule,
		"segment":    "tool",
		"tool_name":  toolName,
		"rules":      strings.Join(ruleIDs(a), ","),
		"blocked":    fmt.Sprintf("%t", blocked),
	}
	evt := pluginsdk.Event{
		PluginName: PluginName,
		Type:       "security.prompt_injection",
		Severity:   severity,
		Message:    fmt.Sprintf("Résultat de l'outil %q %s : risque d'injection %.2f (%s)", toolName, verb, a.Risk, joinCategories(a)),
		Attributes: attrs,
	}
	if reqCtx != nil {
		evt.OrgID = reqCtx.GetOrgId()
		evt.UserID = reqCtx.GetUserId()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := hc.EmitEvent(ctx, evt); err != nil {
			slog.Warn("prompt-guard: could not emit tool-result event", slog.Any("error", err))
		}
	}()
}

func joinCategories(a promptguard.Assessment) string {
	cats := a.CategoryList()
	parts := make([]string, 0, len(cats))
	for _, c := range cats {
		parts = append(parts, string(c))
	}
	return strings.Join(parts, ",")
}

func round3(x float64) float64 {
	return float64(int(x*1000+0.5)) / 1000
}

func (p *Plugin) defaultGuard() *promptguard.Guard {
	g, _ := p.guardFor(Config{})
	return g
}

func (p *Plugin) guardFor(cfg Config) (*promptguard.Guard, error) {
	key := fmt.Sprintf("%v|%v|%v|%s", cfg.ToolWeight, cfg.QuoteDamping, cfg.ModelCap, cfg.ExtraRules)
	p.guardsMu.Lock()
	defer p.guardsMu.Unlock()
	if p.guards == nil {
		p.guards = map[string]*promptguard.Guard{}
	}
	if g, ok := p.guards[key]; ok {
		return g, nil
	}
	rules := promptguard.DefaultRules()
	if strings.TrimSpace(cfg.ExtraRules) != "" {
		extra, err := promptguard.ParseRules([]byte(cfg.ExtraRules))
		if err != nil {
			return nil, err
		}
		rules = rules.Merge(extra)
	}
	g := promptguard.New(promptguard.Options{
		Rules:        rules,
		QuoteDamping: cfg.QuoteDamping,
		NoModel:      cfg.ModelCap == 0,
		ModelCap:     cfg.ModelCap,
		KindWeights: map[promptguard.SegmentKind]float64{
			promptguard.SegmentUser:    1,
			promptguard.SegmentHistory: promptguard.DefaultKindWeights[promptguard.SegmentHistory],
			promptguard.SegmentTool:    cfg.ToolWeight,
		},
	})
	// Bound the cache: a node whose config is edited a hundred times should
	// not keep a hundred compiled sets alive.
	if len(p.guards) >= 16 {
		p.guards = map[string]*promptguard.Guard{}
	}
	p.guards[key] = g
	return g, nil
}

// emit reports a suspicious request as an event. The prompt itself is never
// included: rule ids and scores are enough to investigate, and the raw text
// may carry personal data.
func (p *Plugin) emit(reqCtx *proto.RequestContext, a promptguard.Assessment, blocked bool) {
	hc := p.getHostClient()
	if hc == nil {
		return
	}
	severity := "warning"
	verb := "suspecte"
	if blocked {
		severity = "error"
		verb = "bloquée"
	}
	attrs := map[string]string{
		"risk":       fmt.Sprintf("%.3f", a.Risk),
		"categories": joinCategories(a),
		"top_rule":   a.TopRule,
		"segment":    string(a.Segment),
		"rules":      strings.Join(ruleIDs(a), ","),
		"blocked":    fmt.Sprintf("%t", blocked),
	}
	if a.Structural.InvisibleCount > 0 {
		attrs["invisible_characters"] = fmt.Sprint(a.Structural.InvisibleCount)
	}
	if a.Structural.EncodedPayloads > 0 {
		attrs["encoded_payloads"] = fmt.Sprint(a.Structural.EncodedPayloads)
	}
	evt := pluginsdk.Event{
		PluginName: PluginName,
		Type:       "security.prompt_injection",
		Severity:   severity,
		Message:    fmt.Sprintf("Requête %s : risque d'injection %.2f (%s)", verb, a.Risk, joinCategories(a)),
		Attributes: attrs,
	}
	if reqCtx != nil {
		evt.OrgID = reqCtx.GetOrgId()
		evt.UserID = reqCtx.GetUserId()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := hc.EmitEvent(ctx, evt); err != nil {
			slog.Warn("prompt-guard: could not emit event", slog.Any("error", err))
		}
	}()
}

func ruleIDs(a promptguard.Assessment) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range a.Matches {
		if !seen[m.RuleID] {
			seen[m.RuleID] = true
			out = append(out, m.RuleID)
		}
	}
	return out
}

// ─── Config ───────────────────────────────────────────────────────────────────

type Config struct {
	// BlockAbove rejects the request when risk >= this value. 0 disables.
	BlockAbove float64 `json:"block_above"`
	// BlockMessage is the rejection reason returned to the client.
	BlockMessage string `json:"block_message"`
	// SuspiciousAbove drives the boolean "suspicious" port.
	SuspiciousAbove float64 `json:"suspicious_above"`
	// EventAbove emits a security event when risk >= this value. 0 disables.
	EventAbove float64 `json:"event_above"`
	// AnalyzeToolResults scores tool messages (indirect injection).
	AnalyzeToolResults bool `json:"analyze_tool_results"`
	// AnalyzeHistory scores earlier user turns, not only the last one.
	AnalyzeHistory bool `json:"analyze_history"`
	// ToolWeight multiplies the risk found in tool results.
	ToolWeight float64 `json:"tool_weight"`
	// ExtraRules is a YAML rule file merged over the default rules.
	ExtraRules string `json:"extra_rules"`
	// QuoteDamping multiplies the risk of an attack that is only quoted
	// (translated, analysed). 1 disables.
	QuoteDamping float64 `json:"quote_damping"`
	// ModelCap bounds the contribution of the statistical model. 0 disables
	// the model.
	ModelCap float64 `json:"model_cap"`
}

func defaultConfig() Config {
	return Config{
		BlockAbove:         0,
		BlockMessage:       "Requête refusée : elle ressemble à une tentative de manipulation de l'assistant.",
		SuspiciousAbove:    0.5,
		EventAbove:         0.6,
		AnalyzeToolResults: true,
		AnalyzeHistory:     false,
		ToolWeight:         promptguard.DefaultKindWeights[promptguard.SegmentTool],
		QuoteDamping:       promptguard.DefaultQuoteDamping,
		ModelCap:           promptguard.DefaultModelCap,
	}
}

func parseConfig(raw string) Config {
	cfg := defaultConfig()
	if raw == "" || raw == "{}" {
		return cfg
	}
	var aux struct {
		BlockAbove         *float64 `json:"block_above"`
		BlockMessage       *string  `json:"block_message"`
		SuspiciousAbove    *float64 `json:"suspicious_above"`
		EventAbove         *float64 `json:"event_above"`
		AnalyzeToolResults *bool    `json:"analyze_tool_results"`
		AnalyzeHistory     *bool    `json:"analyze_history"`
		ToolWeight         *float64 `json:"tool_weight"`
		ExtraRules         *string  `json:"extra_rules"`
		QuoteDamping       *float64 `json:"quote_damping"`
		ModelCap           *float64 `json:"model_cap"`
	}
	if err := json.Unmarshal([]byte(raw), &aux); err != nil {
		return cfg
	}
	if aux.BlockAbove != nil && *aux.BlockAbove >= 0 && *aux.BlockAbove <= 1 {
		cfg.BlockAbove = *aux.BlockAbove
	}
	if aux.BlockMessage != nil && strings.TrimSpace(*aux.BlockMessage) != "" {
		cfg.BlockMessage = *aux.BlockMessage
	}
	if aux.SuspiciousAbove != nil && *aux.SuspiciousAbove >= 0 && *aux.SuspiciousAbove <= 1 {
		cfg.SuspiciousAbove = *aux.SuspiciousAbove
	}
	if aux.EventAbove != nil && *aux.EventAbove >= 0 && *aux.EventAbove <= 1 {
		cfg.EventAbove = *aux.EventAbove
	}
	if aux.AnalyzeToolResults != nil {
		cfg.AnalyzeToolResults = *aux.AnalyzeToolResults
	}
	if aux.AnalyzeHistory != nil {
		cfg.AnalyzeHistory = *aux.AnalyzeHistory
	}
	if aux.ToolWeight != nil && *aux.ToolWeight > 0 && *aux.ToolWeight <= 3 {
		cfg.ToolWeight = *aux.ToolWeight
	}
	if aux.ExtraRules != nil {
		cfg.ExtraRules = *aux.ExtraRules
	}
	if aux.QuoteDamping != nil && *aux.QuoteDamping > 0 && *aux.QuoteDamping <= 1 {
		cfg.QuoteDamping = *aux.QuoteDamping
	}
	if aux.ModelCap != nil && *aux.ModelCap >= 0 && *aux.ModelCap <= 1 {
		cfg.ModelCap = *aux.ModelCap
	}
	return cfg
}

const configSchemaJSON = `{
  "type": "object",
  "properties": {
    "block_above": {
      "type": "number",
      "title": "Bloquer au-delà de",
      "description": "Risque à partir duquel la requête est refusée (403). 0 désactive le blocage : le nœud se contente d'exposer les scores.",
      "minimum": 0, "maximum": 1, "default": 0
    },
    "block_message": {
      "type": "string",
      "title": "Message de refus",
      "default": "Requête refusée : elle ressemble à une tentative de manipulation de l'assistant."
    },
    "suspicious_above": {
      "type": "number",
      "title": "Seuil du port « suspicious »",
      "description": "Risque à partir duquel le port booléen suspicious passe à vrai.",
      "minimum": 0, "maximum": 1, "default": 0.5
    },
    "event_above": {
      "type": "number",
      "title": "Émettre un événement au-delà de",
      "description": "Risque à partir duquel un événement security.prompt_injection est enregistré (sans le texte de la requête). 0 désactive.",
      "minimum": 0, "maximum": 1, "default": 0.6
    },
    "analyze_tool_results": {
      "type": "boolean",
      "title": "Analyser les résultats d'outils",
      "description": "Les documents et pages web renvoyés par les outils sont le vecteur des injections indirectes.",
      "default": true
    },
    "analyze_history": {
      "type": "boolean",
      "title": "Analyser les tours précédents",
      "description": "Score aussi les messages utilisateur antérieurs au dernier, pondérés à 0,8.",
      "default": false
    },
    "tool_weight": {
      "type": "number",
      "title": "Pondération des résultats d'outils",
      "description": "Multiplicateur appliqué au risque trouvé dans un résultat d'outil.",
      "minimum": 0.1, "maximum": 3, "default": 1.25
    },
    "quote_damping": {
      "type": "number",
      "title": "Atténuation des attaques citées",
      "description": "Facteur appliqué au risque quand toutes les correspondances sont entre guillemets après un verbe d'encadrement (traduis, explique, classe…). 1 désactive.",
      "minimum": 0.05, "maximum": 1, "default": 0.5
    },
    "model_cap": {
      "type": "number",
      "title": "Plafond du modèle statistique",
      "description": "Contribution maximale du modèle embarqué au risque. En dessous de 0,5 de probabilité il ne contribue pas ; au-dessus, sa probabilité compte jusqu'à ce plafond. 0 désactive le modèle. Un seuil de blocage supérieur au plafond garantit qu'un blocage repose toujours sur une règle ou un signal structurel.",
      "minimum": 0, "maximum": 1, "default": 0.6
    },
    "extra_rules": {
      "type": "string",
      "format": "multiline",
      "title": "Règles supplémentaires (YAML)",
      "description": "Même format que rules.yaml. Une règle portant l'id d'une règle par défaut la remplace, ce qui permet d'ajuster un poids ou de la désactiver (enabled: false)."
    }
  }
}`
