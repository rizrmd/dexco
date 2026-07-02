package builtin

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/rizrmd/dexco/internal/model"
)

// Adapted from Codex current-time coverage at the handler level. Dexco does not
// implement app-server's external clock reminder path, but the built-in tool
// still needs to return a stable UTC RFC3339 value and classify as read-only.
func TestCurrentTimeHandlerReturnsRFC3339UTCAndReadOnlyGuardrail(t *testing.T) {
	t.Parallel()

	handler := CurrentTimeHandler{}
	result, err := handler.Call(context.Background(), model.ToolCall{
		CallID: "call-time",
		Name:   "current_time",
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !result.Success {
		t.Fatalf("Success = false, want true")
	}

	parsed, err := time.Parse(time.RFC3339, result.Output)
	if err != nil {
		t.Fatalf("Output = %q, want RFC3339 UTC time: %v", result.Output, err)
	}
	if parsed.Location() != time.UTC {
		t.Fatalf("Location = %v, want UTC", parsed.Location())
	}

	guardrail, err := handler.Guardrail(context.Background(), model.ToolCall{
		CallID: "call-time",
		Name:   "current_time",
	})
	if err != nil {
		t.Fatalf("Guardrail() error = %v", err)
	}
	want := model.ToolGuardrail{
		Risk:                model.ToolRiskReadOnly,
		ApprovalRequirement: model.ApprovalRequirementNone,
		Reason:              "read-only time lookup",
	}
	if !reflect.DeepEqual(guardrail, want) {
		t.Fatalf("Guardrail() = %#v, want %#v", guardrail, want)
	}
}
