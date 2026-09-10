//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	gormadapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// Plugins built from the working tree and served to the server under test.
// mcp-bridge (needs an MCP server), llm-classifier (needs a model answering
// structured JSON) and fuzzy-evaluator (covered by its own unit tests) are
// left out on purpose.
var e2ePlugins = []string{
	"pseudonymizer",
	"system-prompt",
	"time-restriction",
	"dummy-model",
	"request-inspector",
	"complexity-scorer",
	"text-classifier",
	"energy-estimator",
	"budget-pressure",
	"prompt-guard",
	"script-processor",
}

// Models added on the fake provider. The fake answers any real model name,
// and fails every call to realModelBroken with a 503 so fallback chains can be
// exercised.
const (
	modelFast   = "acme/e2e-fast"
	modelStrong = "acme/e2e-strong"
	modelBroken = "acme/e2e-broken"

	realModelBroken = "e2e-broken"
)

// Virtual models created by the harness, one per scenario family.
const (
	vmRouter       = "acme/e2e-router"        // complexity-scorer → compare → select → model
	vmLogic        = "acme/e2e-logic"         // value, math, compare, select, sample, context, trace
	vmFallback     = "acme/e2e-fallback"      // model_fallback: broken → fast
	vmSystemPrompt = "acme/e2e-system-prompt" // system-prompt plugin
	vmDummy        = "acme/e2e-dummy"         // dummy-model plugin (no upstream call)
	vmClosed       = "acme/e2e-closed"        // time-restriction, always outside the slots
	vmOpen         = "acme/e2e-open"          // time-restriction, always inside the slots
	vmGuard        = "acme/e2e-guard"         // prompt-guard in blocking mode
	vmTelemetry    = "acme/e2e-telemetry"     // request-inspector, text-classifier, energy-estimator, budget-pressure, script-processor → trace
)

const dummyTemplate = "Réponse factice pour {{.User}} : {{.LastMessage}}"

// ── Graph builder ────────────────────────────────────────────────────────────

// graph is a small builder keeping the fixtures readable: nodes are declared
// by type with their data, edges as "node.port" pairs.
type graph struct {
	g     model.PipelineGraph
	edges int
}

func newGraph() *graph { return &graph{} }

func (b *graph) node(id string, typ model.PipelineNodeType, data any) *graph {
	n := model.PipelineNode{ID: id, Type: typ, Position: model.NodePosition{X: float64(len(b.g.Nodes)) * 220, Y: 120}}
	if data != nil {
		n.Data = mustJSON(data)
	}
	b.g.Nodes = append(b.g.Nodes, n)
	return b
}

func (b *graph) generator(id string) *graph { return b.node(id, model.NodeTypeGenerator, nil) }
func (b *graph) sink(id string) *graph      { return b.node(id, model.NodeTypeSink, nil) }

func (b *graph) plugin(id, name, config string) *graph {
	data := model.PluginNodeData{PluginName: name}
	if config != "" {
		data.Config = json.RawMessage(config)
	}
	return b.node(id, model.NodeTypePlugin, data)
}

func (b *graph) modelNode(id, proxyName string) *graph {
	return b.node(id, model.NodeTypeModel, model.ModelNodeData{ProxyName: proxyName})
}

func (b *graph) modelRef(id, proxyName string) *graph {
	return b.node(id, model.NodeTypeModelRef, model.ModelRefNodeData{ProxyName: proxyName})
}

func (b *graph) value(id, portType, value string) *graph {
	return b.node(id, model.NodeTypeValue, model.ValueNodeData{PortType: portType, Value: value})
}

func (b *graph) compare(id, op string, threshold float64) *graph {
	return b.node(id, model.NodeTypeCompare, model.CompareNodeData{Op: op, Threshold: threshold})
}

func (b *graph) selectNode(id string) *graph { return b.node(id, model.NodeTypeSelect, nil) }

func (b *graph) math(id, op string, weights ...float64) *graph {
	return b.node(id, model.NodeTypeMath, model.MathNodeData{Op: op, Weights: weights})
}

func (b *graph) sample(id string, percent float64, key string) *graph {
	return b.node(id, model.NodeTypeSample, model.SampleNodeData{Percent: percent, Key: key, Salt: "e2e"})
}

func (b *graph) context(id string) *graph {
	return b.node(id, model.NodeTypeContext, model.ContextNodeData{Timezone: "UTC"})
}

// trace declares a trace node whose ports are given as "name:type" pairs.
func (b *graph) trace(id, label string, ports ...string) *graph {
	data := model.TraceNodeData{Label: label, Severity: "info"}
	for _, p := range ports {
		name, typ, _ := strings.Cut(p, ":")
		data.Inputs = append(data.Inputs, model.TraceInputDef{Name: name, PortType: typ})
	}
	return b.node(id, model.NodeTypeTrace, data)
}

func (b *graph) fallback(id string, models ...string) *graph {
	return b.node(id, model.NodeTypeModelFallback, model.ModelFallbackNodeData{Models: models})
}

// edge connects "source.port" to "target.port".
func (b *graph) edge(from, to string) *graph {
	src, srcPort, _ := strings.Cut(from, ".")
	dst, dstPort, _ := strings.Cut(to, ".")
	b.edges++
	b.g.Edges = append(b.g.Edges, model.PipelineEdge{
		ID: fmt.Sprintf("e%d", b.edges), Source: src, SourcePort: srcPort, Target: dst, TargetPort: dstPort,
	})
	return b
}

func (b *graph) json() string { return string(mustJSON(b.g)) }

// ── Fixtures ─────────────────────────────────────────────────────────────────

// prepareDatabase points the seeded OpenAI provider at the fake one and adds
// the models, middlewares and virtual models the scenarios rely on.
func prepareDatabase(dsn, providerURL string) error {
	db, err := openDB(dsn)
	if err != nil {
		return err
	}
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	}()

	if err := db.Model(&gormadapter.Provider{}).
		Where("id = ?", providerAcmeOpenAI).
		Update("base_url", providerURL).Error; err != nil {
		return fmt.Errorf("point provider at fake: %w", err)
	}

	if err := createModels(db); err != nil {
		return err
	}
	if err := createMiddlewares(db); err != nil {
		return err
	}
	return createVirtualModels(db)
}

func createModels(db *gorm.DB) error {
	now := time.Now()
	for _, name := range []string{"e2e-fast", "e2e-strong", realModelBroken} {
		m := &gormadapter.LLMModel{
			ID: "mdl-" + name, CreatedAt: now, UpdatedAt: now,
			ProviderID: providerAcmeOpenAI, OrgID: orgAcme,
			ProxyName: name, RealModel: name,
			Description:           "Modèle de test e2e servi par le fournisseur factice.",
			Enabled:               1,
			PromptCostPer1KTokens: 100, CompletionCostPer1KTokens: 200,
			ContextWindow: 32_000, OutputWindow: 4_096, ActiveParams: 8_000_000_000,
			TokensPerSecLow: 50, TokensPerSecHigh: 100,
		}
		if err := db.Create(m).Error; err != nil {
			return fmt.Errorf("create model %s: %w", name, err)
		}
	}
	return nil
}

func createMiddlewares(db *gorm.DB) error {
	middlewares := []*gormadapter.Middleware{
		pseudonymizerMiddleware("mw-e2e-pseudo-tag", "e2e-pseudonymizer-tag", modelAcmeGPT4oMini,
			`{"language":"fr","strategy":"tag"}`),
		pseudonymizerMiddleware("mw-e2e-pseudo-hash", "e2e-pseudonymizer-hash", modelAcmeGPT4o,
			`{"language":"fr","strategy":"hash"}`),
	}
	for _, mw := range middlewares {
		if err := db.Create(mw).Error; err != nil {
			return fmt.Errorf("create middleware %s: %w", mw.ID, err)
		}
	}
	return nil
}

// pseudonymizerMiddleware builds a middleware wrapping a single LLM model with
// a pseudonymizer node: generator → pseudonymizer → model (passthrough) → sink.
func pseudonymizerMiddleware(id, name, targetModelID, pluginConfig string) *gormadapter.Middleware {
	g := newGraph().
		generator("gen").
		plugin("pseudo", "pseudonymizer", pluginConfig).
		node("llm", model.NodeTypeModel, model.ModelNodeData{Passthrough: true}).
		sink("out").
		edge("gen.request", "pseudo.request").
		edge("pseudo.request", "llm.request").
		edge("llm.response", "out.response")
	now := time.Now()
	return &gormadapter.Middleware{
		ID:          id,
		OrgID:       orgAcme,
		Name:        name,
		Enabled:     true,
		Priority:    5,
		TargetsJSON: string(mustJSON([]model.ModelRef{{Kind: model.ModelRefKindLLM, ID: targetModelID}})),
		GraphJSON:   g.json(),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func createVirtualModels(db *gorm.DB) error {
	now := time.Now()
	for name, g := range virtualModelGraphs() {
		short := strings.TrimPrefix(name, "acme/")
		vm := &gormadapter.VirtualModel{
			ID: "vm-" + short, OrgID: orgAcme, Name: short,
			Description: "Modèle virtuel de test e2e.",
			GraphJSON:   g.json(),
			CreatedAt:   now, UpdatedAt: now,
		}
		if err := db.Create(vm).Error; err != nil {
			return fmt.Errorf("create virtual model %s: %w", name, err)
		}
	}
	return nil
}

// virtualModelGraphs declares every scenario pipeline, keyed by proxy name.
func virtualModelGraphs() map[string]*graph {
	return map[string]*graph{
		// Routing by complexity: the scorer feeds compare, select picks a
		// model_ref, and a trace keeps the decision visible.
		vmRouter: newGraph().
			generator("gen").
			plugin("scorer", "complexity-scorer", "").
			compare("cmp", "gt", 0.5).
			modelRef("strong", modelStrong).
			modelRef("fast", modelFast).
			selectNode("sel").
			trace("trace", "router", "complexity:number", "model_name:string").
			modelNode("llm", "").
			sink("out").
			edge("gen.request", "scorer.request").
			edge("scorer.complexity", "cmp.value").
			edge("cmp.result", "sel.condition").
			edge("strong.model_name", "sel.when_true").
			edge("fast.model_name", "sel.when_false").
			edge("sel.value", "llm.model_name").
			edge("scorer.complexity", "trace.complexity").
			edge("sel.value", "trace.model_name").
			edge("gen.request", "llm.request").
			edge("llm.response", "out.response"),

		// Pure built-in logic, fully deterministic: max(0.2, 0.9) > 0.5 picks
		// the strong model; a 0 % sample never selects; context exposes the
		// caller; everything lands in one trace event.
		vmLogic: newGraph().
			generator("gen").
			value("low", "number", "0.2").
			value("high", "number", "0.9").
			math("max", "max").
			compare("cmp", "gt", 0.5).
			modelRef("strong", modelStrong).
			modelRef("fast", modelFast).
			selectNode("sel").
			sample("canary", 0, "user").
			value("new", "string", "canary-model").
			value("old", "string", "stable-model").
			selectNode("canarySel").
			context("ctx").
			trace("trace", "logic", "score:number", "model_name:string", "selected:boolean", "canary:string", "user_id:string", "weekday:string").
			modelNode("llm", "").
			sink("out").
			edge("low.value", "max.a").
			edge("high.value", "max.b").
			edge("max.result", "cmp.value").
			edge("cmp.result", "sel.condition").
			edge("strong.model_name", "sel.when_true").
			edge("fast.model_name", "sel.when_false").
			edge("sel.value", "llm.model_name").
			edge("canary.selected", "canarySel.condition").
			edge("new.value", "canarySel.when_true").
			edge("old.value", "canarySel.when_false").
			edge("max.result", "trace.score").
			edge("sel.value", "trace.model_name").
			edge("canary.selected", "trace.selected").
			edge("canarySel.value", "trace.canary").
			edge("ctx.user_id", "trace.user_id").
			edge("ctx.weekday", "trace.weekday").
			edge("gen.request", "llm.request").
			edge("llm.response", "out.response"),

		vmFallback: newGraph().
			generator("gen").
			fallback("fb", modelBroken, modelFast).
			sink("out").
			edge("gen.request", "fb.request").
			edge("fb.response", "out.response"),

		vmSystemPrompt: newGraph().
			generator("gen").
			plugin("sys", "system-prompt", `{"system_prompt":"Tu es l'assistant e2e."}`).
			modelNode("llm", modelFast).
			sink("out").
			edge("gen.request", "sys.request").
			edge("sys.request", "llm.request").
			edge("llm.response", "out.response"),

		vmDummy: newGraph().
			generator("gen").
			plugin("dummy", "dummy-model", string(mustJSON(map[string]any{"response_template": dummyTemplate}))).
			sink("out").
			edge("gen.request", "dummy.request").
			edge("dummy.response", "out.response"),

		vmClosed: timeRestrictedGraph(false),
		vmOpen:   timeRestrictedGraph(true),

		vmGuard: newGraph().
			generator("gen").
			plugin("guard", "prompt-guard", `{"block_above":0.6,"block_message":"Requête refusée par prompt-guard (e2e)."}`).
			modelNode("llm", modelFast).
			sink("out").
			edge("gen.request", "guard.request").
			edge("gen.request", "llm.request").
			edge("llm.response", "out.response"),

		// Analysis plugins feeding one trace: their outputs become event
		// attributes the test can read back.
		vmTelemetry: newGraph().
			generator("gen").
			plugin("inspect", "request-inspector", "").
			plugin("classify", "text-classifier", "").
			plugin("energy", "energy-estimator", "").
			plugin("budget", "budget-pressure", "").
			plugin("script", "script-processor", string(mustJSON(map[string]any{
				"script":  `export func(ctx) { return { outputs: { double: ctx.inputs.n * 2 } } }`,
				"inputs":  []map[string]string{{"name": "n", "portType": "number"}},
				"outputs": []map[string]string{{"name": "double", "portType": "number"}},
			}))).
			trace("trace", "telemetry",
				"message_count:number", "input_tokens:number", "double:number",
				"category:string", "source:string",
				"energy_wh:number", "has_budget:boolean").
			modelNode("llm", modelFast).
			sink("out").
			edge("gen.request", "inspect.request").
			edge("gen.request", "classify.request").
			edge("gen.request", "budget.request").
			edge("inspect.input_tokens", "energy.input_tokens").
			edge("inspect.message_count", "script.n").
			edge("inspect.message_count", "trace.message_count").
			edge("inspect.input_tokens", "trace.input_tokens").
			edge("script.double", "trace.double").
			edge("classify.category", "trace.category").
			edge("classify.source", "trace.source").
			edge("energy.energy_wh", "trace.energy_wh").
			edge("budget.has_budget", "trace.has_budget").
			edge("gen.request", "llm.request").
			edge("llm.response", "out.response"),
	}
}

// timeRestrictedGraph wraps the fast model with a time-restriction node whose
// slots either cover the whole week (open) or only a day that is not today in
// UTC (closed), so the outcome does not depend on when the suite runs.
func timeRestrictedGraph(open bool) *graph {
	days := []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}
	if !open {
		today := strings.ToLower(time.Now().UTC().Weekday().String())
		for i, d := range days {
			if d != today {
				days = days[i : i+1]
				break
			}
		}
	}
	cfg := mustJSON(map[string]any{
		"timezone": "UTC",
		"slots":    []map[string]any{{"days": days, "start": "00:00", "end": "23:59"}},
	})
	return newGraph().
		generator("gen").
		plugin("hours", "time-restriction", string(cfg)).
		modelNode("llm", modelFast).
		sink("out").
		edge("gen.request", "hours.request").
		edge("hours.request", "llm.request").
		edge("llm.response", "out.response")
}
