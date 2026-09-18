package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

func TestBlockExecutor_LetsThroughWhenFalse(t *testing.T) {
	e := NewBlockExecutor(nil)
	for _, cond := range []interface{}{false, 0.0} {
		res, err := e.Forward(context.Background(), nodeWith(model.NodeTypeBlock, ""), map[string]interface{}{"condition": cond}, ExecutionContext{})
		if err != nil {
			t.Fatalf("condition %v: %v", cond, err)
		}
		if res.Rejected {
			t.Errorf("condition %v: request must not be rejected", cond)
		}
	}
}

func TestBlockExecutor_RejectsWithMessageAndEvent(t *testing.T) {
	em := &recordingEmitter{}
	e := NewBlockExecutor(em)
	node := nodeWith(model.NodeTypeBlock, `{"label":"policy","message":"Hors périmètre."}`)
	ec := ExecutionContext{OrgID: "org-1", UserID: "user-1"}

	res, err := e.Forward(context.Background(), node, map[string]interface{}{"condition": true}, ec)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rejected || res.RejectionReason != "Hors périmètre." {
		t.Fatalf("expected rejection with the configured message, got %+v", res)
	}
	if len(em.events) != 1 {
		t.Fatalf("expected one event, got %d", len(em.events))
	}
	evt := em.events[0]
	if evt.Type() != "request.blocked" {
		t.Errorf("event type = %q, want request.blocked", evt.Type())
	}
	if evt.Severity() != model.SeverityWarning {
		t.Errorf("severity = %q, want warning", evt.Severity())
	}
	if evt.OrgID() != "org-1" || evt.UserID() != "user-1" {
		t.Errorf("event not attributed to the caller: org=%q user=%q", evt.OrgID(), evt.UserID())
	}
	attrs := evt.Attributes()
	if attrs["reason"] != "Hors périmètre." || attrs["label"] != "policy" || attrs["node_id"] != "n" {
		t.Errorf("unexpected attributes: %v", attrs)
	}
}

func TestBlockExecutor_DefaultMessageAndNumericCondition(t *testing.T) {
	e := NewBlockExecutor(nil)
	res, err := e.Forward(context.Background(), nodeWith(model.NodeTypeBlock, ""), map[string]interface{}{"condition": 1.0}, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rejected || res.RejectionReason != DefaultBlockMessage {
		t.Fatalf("expected rejection with the default message, got %+v", res)
	}
}

func TestBlockExecutor_UnconfiguredNodeEmitsDefaultReason(t *testing.T) {
	em := &recordingEmitter{}
	e := NewBlockExecutor(em)
	res, err := e.Forward(context.Background(), nodeWith(model.NodeTypeBlock, ""), map[string]interface{}{"condition": true}, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rejected || res.RejectionReason != DefaultBlockMessage {
		t.Fatalf("expected rejection with the default message, got %+v", res)
	}
	if len(em.events) != 1 {
		t.Fatalf("expected one event, got %d", len(em.events))
	}
	attrs := em.events[0].Attributes()
	if attrs["reason"] != DefaultBlockMessage {
		t.Errorf("reason = %q, want the default message", attrs["reason"])
	}
	if _, has := attrs["label"]; has {
		t.Errorf("an unlabelled node must not record a label attribute: %v", attrs)
	}
}

func TestBlockExecutor_UnconnectedConditionFails(t *testing.T) {
	e := NewBlockExecutor(nil)
	_, err := e.Forward(context.Background(), nodeWith(model.NodeTypeBlock, ""), map[string]interface{}{}, ExecutionContext{})
	if err == nil || !strings.Contains(err.Error(), "condition") {
		t.Fatalf("expected a condition error, got %v", err)
	}
	_, err = e.Forward(context.Background(), nodeWith(model.NodeTypeBlock, ""), map[string]interface{}{"condition": "yes"}, ExecutionContext{})
	if err == nil {
		t.Fatal("a string condition must fail")
	}
}
