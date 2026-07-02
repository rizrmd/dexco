package builtin

import (
	"context"
	"time"

	"github.com/rizrmd/dexco/internal/model"
)

type CurrentTimeHandler struct{}

func (CurrentTimeHandler) Name() string {
	return "current_time"
}

func (CurrentTimeHandler) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        "current_time",
		Description: "Returns the current UTC time in RFC3339 format.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (CurrentTimeHandler) Guardrail(context.Context, model.ToolCall) (model.ToolGuardrail, error) {
	return model.ToolGuardrail{
		Risk:                model.ToolRiskReadOnly,
		ApprovalRequirement: model.ApprovalRequirementNone,
		Reason:              "read-only time lookup",
	}, nil
}

func (CurrentTimeHandler) Call(context.Context, model.ToolCall) (model.ToolResult, error) {
	return model.ToolResult{
		Output:  time.Now().UTC().Format(time.RFC3339),
		Success: true,
	}, nil
}
