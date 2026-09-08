package pipeline

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

func nodeWith(t model.PipelineNodeType, data string) model.PipelineNode {
	n := model.PipelineNode{ID: "n", Type: t}
	if data != "" {
		n.Data = json.RawMessage(data)
	}
	return n
}

func TestCompareExecutor(t *testing.T) {
	e := NewCompareExecutor()
	cases := []struct {
		data   string
		inputs map[string]interface{}
		want   bool
	}{
		{`{"op":"gt","threshold":0.5}`, map[string]interface{}{"value": 0.7}, true},
		{`{"op":"gt","threshold":0.5}`, map[string]interface{}{"value": 0.5}, false},
		{`{"op":"gte","threshold":0.5}`, map[string]interface{}{"value": 0.5}, true},
		{`{"op":"lt","threshold":0.5}`, map[string]interface{}{"value": 0.2}, true},
		{`{"op":"eq","threshold":1}`, map[string]interface{}{"value": 1.0}, true},
		{`{"op":"ne","threshold":1}`, map[string]interface{}{"value": 1.0}, false},
		// A connected threshold port wins over the configured one.
		{`{"op":"gt","threshold":0.9}`, map[string]interface{}{"value": 0.7, "threshold": 0.5}, true},
		// Default op is gt.
		{``, map[string]interface{}{"value": 1.0}, true},
	}
	for _, c := range cases {
		res, err := e.Forward(context.Background(), nodeWith(model.NodeTypeCompare, c.data), c.inputs, ExecutionContext{})
		if err != nil {
			t.Fatalf("%s %v: %v", c.data, c.inputs, err)
		}
		if res.OutputValues["result"] != c.want {
			t.Errorf("%s %v: expected %v, got %v", c.data, c.inputs, c.want, res.OutputValues["result"])
		}
	}
	if _, err := e.Forward(context.Background(), nodeWith(model.NodeTypeCompare, ""), map[string]interface{}{}, ExecutionContext{}); err == nil {
		t.Error("missing value must fail")
	}
	if _, err := e.Forward(context.Background(), nodeWith(model.NodeTypeCompare, `{"op":"between"}`), map[string]interface{}{"value": 1.0}, ExecutionContext{}); err == nil {
		t.Error("unknown op must fail")
	}
}

func TestSelectExecutor(t *testing.T) {
	e := NewSelectExecutor()
	in := map[string]interface{}{"condition": true, "when_true": "org/big", "when_false": "org/small"}
	res, err := e.Forward(context.Background(), nodeWith(model.NodeTypeSelect, ""), in, ExecutionContext{})
	if err != nil || res.OutputValues["value"] != "org/big" {
		t.Fatalf("true branch: %v %v", res, err)
	}
	in["condition"] = false
	res, _ = e.Forward(context.Background(), nodeWith(model.NodeTypeSelect, ""), in, ExecutionContext{})
	if res.OutputValues["value"] != "org/small" {
		t.Errorf("false branch: %v", res.OutputValues)
	}
	delete(in, "when_false")
	if _, err := e.Forward(context.Background(), nodeWith(model.NodeTypeSelect, ""), in, ExecutionContext{}); err == nil {
		t.Error("missing branch must fail")
	}
}

func TestMathExecutor(t *testing.T) {
	e := NewMathExecutor()
	in := map[string]interface{}{"a": 1.0, "b": 2.0, "d": 6.0}
	cases := map[string]float64{
		`{"op":"sum"}`:                          9,
		`{"op":"avg"}`:                          3,
		`{"op":"min"}`:                          1,
		`{"op":"max"}`:                          6,
		`{"op":"product"}`:                      12,
		`{"op":"weighted","weights":[1,1,1,0]}`: 1.5, // (1+2+0*6)/(1+1+0)
		``:                                      9,
	}
	for data, want := range cases {
		res, err := e.Forward(context.Background(), nodeWith(model.NodeTypeMath, data), in, ExecutionContext{})
		if err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		if res.OutputValues["result"] != want {
			t.Errorf("%s: expected %v, got %v", data, want, res.OutputValues["result"])
		}
	}
	if _, err := e.Forward(context.Background(), nodeWith(model.NodeTypeMath, ""), map[string]interface{}{}, ExecutionContext{}); err == nil {
		t.Error("no input must fail")
	}
}

func TestSampleExecutor_StablePerUser(t *testing.T) {
	e := NewSampleExecutor()
	node := nodeWith(model.NodeTypeSample, `{"percent":50,"key":"user","salt":"x"}`)
	first, _ := e.Forward(context.Background(), node, nil, ExecutionContext{UserID: "alice"})
	second, _ := e.Forward(context.Background(), node, nil, ExecutionContext{UserID: "alice"})
	if first.OutputValues["bucket"] != second.OutputValues["bucket"] {
		t.Error("the same user must land in the same bucket")
	}
	b := first.OutputValues["bucket"].(float64)
	if b < 0 || b >= 100 {
		t.Errorf("bucket out of range: %v", b)
	}
	if first.OutputValues["selected"] != (b < 50) {
		t.Errorf("selected must follow the bucket: %v", first.OutputValues)
	}

	// Over many users roughly the configured share is selected.
	selected := 0
	for i := 0; i < 2000; i++ {
		res, _ := e.Forward(context.Background(), node, nil, ExecutionContext{UserID: "user-" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26))})
		if res.OutputValues["selected"] == true {
			selected++
		}
	}
	if selected < 800 || selected > 1200 {
		t.Errorf("expected about 50%% selected, got %d/2000", selected)
	}
}

func TestSampleExecutor_ZeroAndFull(t *testing.T) {
	e := NewSampleExecutor()
	none, _ := e.Forward(context.Background(), nodeWith(model.NodeTypeSample, `{"percent":0,"key":"random"}`), nil, ExecutionContext{})
	all, _ := e.Forward(context.Background(), nodeWith(model.NodeTypeSample, `{"percent":100,"key":"token"}`), nil, ExecutionContext{TokenID: "t"})
	if none.OutputValues["selected"] != false || all.OutputValues["selected"] != true {
		t.Errorf("0%% selects nothing and 100%% everything: %v %v", none.OutputValues, all.OutputValues)
	}
}

func TestContextExecutor(t *testing.T) {
	e := NewContextExecutor()
	e.now = func() time.Time { return time.Date(2026, 9, 5, 22, 30, 0, 0, time.UTC) } // a Saturday, 22:30 UTC
	ec := ExecutionContext{UserID: "u", OrgID: "o", TokenID: "t", DisplayName: "Ada", TargetModelName: "org/x"}

	res, err := e.Forward(context.Background(), nodeWith(model.NodeTypeContext, `{"timezone":"Europe/Paris"}`), nil, ec)
	if err != nil {
		t.Fatal(err)
	}
	out := res.OutputValues
	if out["user_id"] != "u" || out["org_id"] != "o" || out["token_id"] != "t" || out["display_name"] != "Ada" || out["requested_model"] != "org/x" {
		t.Errorf("identity outputs: %v", out)
	}
	if out["hour"] != 0.5 || out["weekday"] != "sunday" || out["is_weekend"] != true {
		t.Errorf("expected Sunday 00:30 in Paris, got %v", out)
	}
	if _, err := e.Forward(context.Background(), nodeWith(model.NodeTypeContext, `{"timezone":"Mars/Olympus"}`), nil, ec); err == nil {
		t.Error("unknown timezone must fail")
	}
}

func TestNoteExecutor(t *testing.T) {
	res, err := NewNoteExecutor().Forward(context.Background(), nodeWith(model.NodeTypeNote, `{"text":"hello"}`), nil, ExecutionContext{})
	if err != nil || len(res.OutputValues) != 0 {
		t.Errorf("note must be inert: %v %v", res, err)
	}
}

type recordingEmitter struct{ events []model.Event }

func (r *recordingEmitter) Emit(_ context.Context, e model.Event) { r.events = append(r.events, e) }

func TestTraceExecutor(t *testing.T) {
	em := &recordingEmitter{}
	e := NewTraceExecutor(em)
	in := map[string]interface{}{"s1": "code", "n1": 0.75, "b1": true}
	_, err := e.Forward(context.Background(), model.PipelineNode{ID: "tr", Type: model.NodeTypeTrace, Data: json.RawMessage(`{"label":"after scoring","severity":"warning"}`)}, in, ExecutionContext{OrgID: "o", UserID: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if len(em.events) != 1 {
		t.Fatalf("expected one event, got %d", len(em.events))
	}
	ev := em.events[0]
	if ev.Type() != "pipeline.trace" || ev.Severity() != model.SeverityWarning || ev.Message() != "after scoring" {
		t.Errorf("unexpected event: type=%s sev=%s msg=%s", ev.Type(), ev.Severity(), ev.Message())
	}
	attrs := ev.Attributes()
	if attrs["port.s1"] != "code" || attrs["port.n1"] != "0.75" || attrs["port.b1"] != "true" || attrs["node_id"] != "tr" {
		t.Errorf("unexpected attributes: %v", attrs)
	}

	// Without an emitter the node still runs.
	if _, err := NewTraceExecutor(nil).Forward(context.Background(), nodeWith(model.NodeTypeTrace, ""), in, ExecutionContext{}); err != nil {
		t.Error(err)
	}
}
