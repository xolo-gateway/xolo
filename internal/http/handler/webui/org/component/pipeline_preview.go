package component

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/a-h/templ"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/templui/component/icon"
)

// graphNodeCount and graphEdgeCount count a possibly-nil graph: a virtual model
// created but never opened in the editor has none.
func GraphNodeCount(graph *model.PipelineGraph) int {
	if graph == nil {
		return 0
	}
	return len(graph.Nodes)
}

func GraphEdgeCount(graph *model.PipelineGraph) int {
	if graph == nil {
		return 0
	}
	return len(graph.Edges)
}

// pluralize renders "3 nœuds" / "1 nœud". French agreement is on the count, so
// the caller passes both forms rather than a suffix.
func Pluralize(n int, singular, plural string) string {
	if n <= 1 {
		return strconv.Itoa(n) + " " + singular
	}
	return strconv.Itoa(n) + " " + plural
}

// PipelineNodeCard is one box of the read-only pipeline preview shown next to a
// virtual model. It flattens a model.PipelineNode into what the mockup draws:
// a kind, a name and a one-line detail.
type PipelineNodeCard struct {
	Kind   string // "Entrée", "Modèle", "Plugin"…
	Name   string
	Detail string
	Icon   func(...icon.Props) templ.Component
	// TintClass is the utility pair of the node's icon square, carrying the kind
	// of the node the way the mockup colours it.
	TintClass string
}

// PipelineCards flattens a graph into the left-to-right chain of the preview.
//
// The editor stores a free-form dataflow graph, so there is no single "the"
// order; the preview reads the canvas the way a user does — by X position, then
// by Y for nodes stacked in the same column (a fallback branch, typically).
func PipelineCards(graph *model.PipelineGraph) []PipelineNodeCard {
	if graph == nil || len(graph.Nodes) == 0 {
		return nil
	}

	nodes := make([]model.PipelineNode, len(graph.Nodes))
	copy(nodes, graph.Nodes)
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Position.X != nodes[j].Position.X {
			return nodes[i].Position.X < nodes[j].Position.X
		}
		return nodes[i].Position.Y < nodes[j].Position.Y
	})

	cards := make([]PipelineNodeCard, 0, len(nodes))
	for _, n := range nodes {
		cards = append(cards, pipelineCard(n))
	}

	return cards
}

func pipelineCard(n model.PipelineNode) PipelineNodeCard {
	switch n.Type {
	case model.NodeTypeGenerator:
		return PipelineNodeCard{
			Kind:      "Entrée",
			Name:      "requête",
			Detail:    "Requête reçue du client",
			Icon:      icon.LogIn,
			TintClass: "bg-muted text-muted-foreground",
		}

	case model.NodeTypeSink:
		return PipelineNodeCard{
			Kind:      "Sortie",
			Name:      "réponse",
			Detail:    "Réponse renvoyée au client",
			Icon:      icon.LogOut,
			TintClass: "bg-muted text-muted-foreground",
		}

	case model.NodeTypeModel:
		var data model.ModelNodeData
		_ = json.Unmarshal(n.Data, &data)
		card := PipelineNodeCard{
			Kind:      "Modèle",
			Name:      data.ProxyName,
			Detail:    "Appel au modèle amont",
			Icon:      icon.BrainCircuit,
			TintClass: "bg-primary-tint text-primary",
		}
		// A passthrough node has no fixed proxy name: it resolves whichever model
		// the caller asked for, which is how a middleware wraps arbitrary models.
		if data.Passthrough {
			card.Name = "modèle demandé"
			card.Detail = "Résolu à l'exécution"
		}
		if card.Name == "" {
			card.Name = "non configuré"
		}
		return card

	case model.NodeTypeModelRef:
		var data model.ModelRefNodeData
		_ = json.Unmarshal(n.Data, &data)
		name := data.ProxyName
		if name == "" {
			name = "non configuré"
		}
		return PipelineNodeCard{
			Kind:      "Modèle (référence)",
			Name:      name,
			Detail:    "Nom de modèle émis",
			Icon:      icon.BrainCircuit,
			TintClass: "bg-primary-tint text-primary",
		}

	case model.NodeTypeModelFallback:
		var data model.ModelFallbackNodeData
		_ = json.Unmarshal(n.Data, &data)
		name := "non configuré"
		if len(data.Models) > 0 {
			name = data.Models[0]
		}
		return PipelineNodeCard{
			Kind:      "Modèle avec repli",
			Name:      name,
			Detail:    fmt.Sprintf("%d modèle(s) en cascade", len(data.Models)),
			Icon:      icon.BrainCircuit,
			TintClass: "bg-primary-tint text-primary",
		}

	case model.NodeTypeCompare, model.NodeTypeSelect, model.NodeTypeMath, model.NodeTypeSample, model.NodeTypeContext:
		var data struct {
			Label string `json:"label"`
		}
		_ = json.Unmarshal(n.Data, &data)
		name := data.Label
		if name == "" {
			name = string(n.Type)
		}
		return PipelineNodeCard{
			Kind:      "Logique",
			Name:      name,
			Detail:    "Nœud intégré",
			Icon:      icon.Hash,
			TintClass: "bg-chart-4/15 text-chart-4",
		}

	case model.NodeTypeTrace:
		var data model.TraceNodeData
		_ = json.Unmarshal(n.Data, &data)
		name := data.Label
		if name == "" {
			name = "trace"
		}
		return PipelineNodeCard{
			Kind:      "Trace",
			Name:      name,
			Detail:    "Événement pipeline.trace",
			Icon:      icon.Hash,
			TintClass: "bg-muted text-muted-foreground",
		}

	case model.NodeTypeNote:
		var data model.NoteNodeData
		_ = json.Unmarshal(n.Data, &data)
		name := data.Text
		if len(name) > 40 {
			name = name[:40] + "…"
		}
		if name == "" {
			name = "(vide)"
		}
		return PipelineNodeCard{
			Kind:      "Note",
			Name:      name,
			Detail:    "Sans effet à l'exécution",
			Icon:      icon.Hash,
			TintClass: "bg-muted text-muted-foreground",
		}

	case model.NodeTypeValue:
		var data model.ValueNodeData
		_ = json.Unmarshal(n.Data, &data)
		name := data.Value
		if name == "" {
			name = "(vide)"
		}
		return PipelineNodeCard{
			Kind:      "Valeur",
			Name:      name,
			Detail:    data.PortType,
			Icon:      icon.Hash,
			TintClass: "bg-chart-4/15 text-chart-4",
		}

	case model.NodeTypePlugin:
		var data model.PluginNodeData
		_ = json.Unmarshal(n.Data, &data)
		name := data.PluginName
		if name == "" {
			name = "non configuré"
		}
		return PipelineNodeCard{
			Kind:      "Plugin",
			Name:      name,
			Detail:    "Traitement intercalé",
			Icon:      icon.Puzzle,
			TintClass: "bg-chart-3/15 text-chart-3",
		}

	default:
		return PipelineNodeCard{
			Kind:      string(n.Type),
			Name:      n.ID,
			Icon:      icon.Box,
			TintClass: "bg-muted text-muted-foreground",
		}
	}
}
