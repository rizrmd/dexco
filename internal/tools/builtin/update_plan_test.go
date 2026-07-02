package builtin

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/rizrmd/dexco/internal/model"
)

// Adapted from Codex core's update_plan handler tests. Codex emits a
// PlanUpdate event for clients but only sends "Plan updated" back to the model;
// Dexco stores that structured event payload on ToolResult so the runner can
// preserve the same split at the library boundary.
func TestUpdatePlanHandlerReturnsPlanUpdateMetadata(t *testing.T) {
	t.Parallel()

	handler := UpdatePlanHandler{}
	result, err := handler.Call(context.Background(), model.ToolCall{
		CallID: "plan-call",
		Name:   "update_plan",
		Arguments: json.RawMessage(`{
			"explanation": "Tracking implementation",
			"plan": [{
				"step": "Implement handler",
				"status": "completed"
			}, {
				"step": "Run tests",
				"status": "in_progress"
			}]
		}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	want := model.ToolResult{
		CallID:  "plan-call",
		Name:    "update_plan",
		Output:  "Plan updated",
		Success: true,
		PlanUpdate: &model.PlanUpdate{
			Explanation: "Tracking implementation",
			Plan: []model.PlanStep{
				{Step: "Implement handler", Status: model.PlanStepCompleted},
				{Step: "Run tests", Status: model.PlanStepInProgress},
			},
		},
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

// Adapted from Codex's malformed update_plan payload coverage. A missing plan
// must be a failed tool result, not a client PlanUpdate event; the handler
// returns the same model-visible parse prefix the router later preserves.
func TestUpdatePlanHandlerRejectsMalformedPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "missing plan", raw: `{"explanation":"missing"}`, want: "missing required field plan"},
		{name: "unknown field", raw: `{"plan":[],"extra":true}`, want: `unknown field "extra"`},
		{name: "invalid status", raw: `{"plan":[{"step":"One","status":"blocked"}]}`, want: "is not supported"},
		{name: "empty step", raw: `{"plan":[{"step":" ","status":"pending"}]}`, want: "step is required"},
		{name: "multiple in progress", raw: `{"plan":[{"step":"One","status":"in_progress"},{"step":"Two","status":"in_progress"}]}`, want: "at most one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := UpdatePlanHandler{}.Call(context.Background(), model.ToolCall{
				CallID:    "plan-call",
				Name:      "update_plan",
				Arguments: json.RawMessage(tt.raw),
			})
			if err == nil {
				t.Fatalf("Call() error = nil, want error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), "failed to parse function arguments") ||
				!strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Call() error = %q, want parse error containing %q", err, tt.want)
			}
		})
	}
}

// Adapted from Codex's update_plan tool spec shape. Dexco does not duplicate
// the generated Rust schema type, but it must expose the same required plan
// field and status enum so Codex-style tool calls are discoverable and valid.
func TestUpdatePlanHandlerSpecAndGuardrail(t *testing.T) {
	t.Parallel()

	handler := UpdatePlanHandler{}
	spec := handler.Spec()
	if spec.Name != "update_plan" {
		t.Fatalf("Name = %q, want update_plan", spec.Name)
	}
	required, ok := spec.Parameters["required"].([]string)
	if !ok || !reflect.DeepEqual(required, []string{"plan"}) {
		t.Fatalf("required = %#v, want [plan]", spec.Parameters["required"])
	}
	properties, ok := spec.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v, want map", spec.Parameters["properties"])
	}
	planProperty, ok := properties["plan"].(map[string]any)
	if !ok {
		t.Fatalf("plan property = %#v, want map", properties["plan"])
	}
	items, ok := planProperty["items"].(map[string]any)
	if !ok {
		t.Fatalf("plan items = %#v, want map", planProperty["items"])
	}
	itemProperties, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("item properties = %#v, want map", items["properties"])
	}
	statusProperty, ok := itemProperties["status"].(map[string]any)
	if !ok {
		t.Fatalf("status property = %#v, want map", itemProperties["status"])
	}
	wantEnum := []string{"pending", "in_progress", "completed"}
	if got := statusProperty["enum"]; !reflect.DeepEqual(got, wantEnum) {
		t.Fatalf("status enum = %#v, want %#v", got, wantEnum)
	}

	guardrail, err := handler.Guardrail(context.Background(), model.ToolCall{
		CallID: "plan-call",
		Name:   "update_plan",
	})
	if err != nil {
		t.Fatalf("Guardrail() error = %v", err)
	}
	wantGuardrail := model.ToolGuardrail{
		Risk:                model.ToolRiskReadOnly,
		ApprovalRequirement: model.ApprovalRequirementNone,
		Reason:              "updates task plan",
	}
	if !reflect.DeepEqual(guardrail, wantGuardrail) {
		t.Fatalf("Guardrail() = %#v, want %#v", guardrail, wantGuardrail)
	}
}
