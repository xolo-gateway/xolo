package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/d5/tengo/v2"
	"github.com/d5/tengo/v2/stdlib"
	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	"github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

const PluginName = "script-processor"
const PluginVersion = "0.1.0"

// runnerScript imports the user's module, calls it with ctx, and stores the result.
// Uses := at top-level so the global pre-declaration is updated.
const runnerScript = `proc := import("process"); result = proc(ctx)`

// Plugin implements the script-processor gRPC plugin.
// The user provides a Tengo module that exports func(ctx) returning a map:
//
//	{ outputs: {portName: value, ...}, messages: [...] }
//
// ctx.request contains the full LLM request body.
// ctx.inputs contains the connected input port values.
type Plugin struct {
	proto.UnimplementedXoloPluginServer
}

func (p *Plugin) Describe(_ context.Context, _ *proto.DescribeRequest) (*proto.PluginDescriptor, error) {
	return &proto.PluginDescriptor{
		Name:        PluginName,
		Version:     PluginVersion,
		Description: "Exécute un script Tengo sur la requête LLM. Entrées et sorties configurables. Peut modifier les messages.",
		Capabilities: []proto.PluginDescriptor_Capability{
			proto.PluginDescriptor_PRE_REQUEST,
		},
		InputPorts:   []*proto.PortDescriptor{},
		OutputPorts:  []*proto.PortDescriptor{},
		ConfigSchema: configSchemaJSON,
	}, nil
}

func (p *Plugin) PreRequest(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
	cfg, err := parseConfig(in.GetCtx().GetConfigJson())
	if err != nil || cfg.Script == "" {
		return &proto.PreRequestOutput{Allowed: true}, nil
	}

	// Build ctx.inputs from connected input port values.
	inputsMap, err := pluginsdk.PreRequestInputs(in)
	if err != nil {
		return nil, fmt.Errorf("script-processor: parse inputs_json: %w", err)
	}

	// Build ctx.request from the full LLM request body entering the node (in.Model).
	requestMap := make(map[string]interface{})
	if in.GetModel() != "" {
		if err := json.Unmarshal([]byte(in.GetModel()), &requestMap); err != nil {
			// If the request body can't be parsed, provide an empty map.
			requestMap = make(map[string]interface{})
		}
	}

	ctx := map[string]interface{}{
		"request": requestMap,
		"inputs":  inputsMap,
	}

	// Register user script as a source module named "process".
	mods := stdlib.GetModuleMap("json", "math", "text", "rand", "times")
	mods.AddSourceModule("process", []byte(cfg.Script))

	s := tengo.NewScript([]byte(runnerScript))
	s.SetImports(mods)
	s.SetMaxAllocs(1 << 20)

	if err := s.Add("ctx", ctx); err != nil {
		return nil, fmt.Errorf("script-processor: inject ctx: %w", err)
	}
	if err := s.Add("result", map[string]interface{}{}); err != nil {
		return nil, fmt.Errorf("script-processor: init result: %w", err)
	}

	compiled, err := s.Compile()
	if err != nil {
		return nil, fmt.Errorf("script-processor: compile: %w", err)
	}
	if err := compiled.Run(); err != nil {
		return nil, fmt.Errorf("script-processor: run: %w", err)
	}

	resultVal, ok := compiled.Get("result").Value().(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("script-processor: script must return a map {outputs: {...}, messages: [...]}")
	}

	out := &proto.PreRequestOutput{Allowed: true}

	// Extract output port values from result.outputs.
	if outputsRaw, ok := resultVal["outputs"].(map[string]interface{}); ok && len(outputsRaw) > 0 {
		filtered := make(map[string]interface{}, len(cfg.Outputs))
		for _, def := range cfg.Outputs {
			if v, exists := outputsRaw[def.Name]; exists {
				filtered[def.Name] = v
			}
		}
		if len(filtered) > 0 {
			b, _ := json.Marshal(filtered)
			out.OutputsJson = string(b)
		}
	}

	// Extract modified messages from result.messages.
	if msgsRaw, ok := resultVal["messages"]; ok && msgsRaw != nil {
		b, err := json.Marshal(msgsRaw)
		if err == nil {
			out.ModifiedMessagesJson = string(b)
		}
	}

	return out, nil
}

// ─── Config ───────────────────────────────────────────────────────────────────

type PortDef struct {
	Name     string `json:"name"`
	PortType string `json:"portType"`
}

type Config struct {
	Script  string    `json:"script"`
	Inputs  []PortDef `json:"inputs,omitempty"`
	Outputs []PortDef `json:"outputs,omitempty"`
}

func parseConfig(raw string) (Config, error) {
	if raw == "" || raw == "{}" {
		return Config{}, nil
	}
	var cfg Config
	return cfg, json.Unmarshal([]byte(raw), &cfg)
}

const configSchemaJSON = `{
  "type": "object",
  "required": ["script"],
  "properties": {
    "script": { "type": "string", "title": "Script Tengo" },
    "inputs": {
      "type": "array",
      "title": "Entrées",
      "items": {
        "type": "object",
        "properties": {
          "name":     { "type": "string" },
          "portType": { "type": "string", "enum": ["number","string","boolean","request","response"] }
        }
      }
    },
    "outputs": {
      "type": "array",
      "title": "Sorties",
      "items": {
        "type": "object",
        "properties": {
          "name":     { "type": "string" },
          "portType": { "type": "string", "enum": ["number","string","boolean"] }
        }
      }
    }
  }
}`
