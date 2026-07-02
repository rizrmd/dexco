package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/rizrmd/dexco/internal/model"
)

type UpdatePlanHandler struct{}

type updatePlanArgs struct {
	Explanation string            `json:"explanation,omitempty"`
	Plan        *[]model.PlanStep `json:"plan"`
}

func (UpdatePlanHandler) Name() string {
	return "update_plan"
}

func (UpdatePlanHandler) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name: "update_plan",
		Description: "Updates the task plan.\n" +
			"Provide an optional explanation and a list of plan items, each with a step and status.\n" +
			"At most one step can be in_progress at a time.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"explanation": map[string]any{
					"type":        "string",
					"description": "Optional explanation for this plan update.",
				},
				"plan": map[string]any{
					"type":        "array",
					"description": "The list of plan items.",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"step": map[string]any{
								"type":        "string",
								"description": "Task step text.",
							},
							"status": map[string]any{
								"type":        "string",
								"description": "Step status.",
								"enum": []string{
									string(model.PlanStepPending),
									string(model.PlanStepInProgress),
									string(model.PlanStepCompleted),
								},
							},
						},
						"required": []string{"step", "status"},
					},
				},
			},
			"required": []string{"plan"},
		},
	}
}

func (UpdatePlanHandler) Guardrail(context.Context, model.ToolCall) (model.ToolGuardrail, error) {
	return model.ToolGuardrail{
		Risk:                model.ToolRiskReadOnly,
		ApprovalRequirement: model.ApprovalRequirementNone,
		Reason:              "updates task plan",
	}, nil
}

func (UpdatePlanHandler) Call(_ context.Context, call model.ToolCall) (model.ToolResult, error) {
	planUpdate, err := parseUpdatePlanArgs(call.Arguments)
	if err != nil {
		return model.ToolResult{}, err
	}

	// Codex's update_plan handler emits a structured PlanUpdate event for the
	// client and returns a small fixed string to the model. Dexco mirrors that
	// split by carrying the event metadata on ToolResult until the runner emits
	// ClientEventPlanUpdate before the ordinary tool_result event.
	return model.ToolResult{
		CallID:     call.CallID,
		Name:       call.Name,
		Output:     "Plan updated",
		Success:    true,
		PlanUpdate: planUpdate,
	}, nil
}

func parseUpdatePlanArgs(raw json.RawMessage) (*model.PlanUpdate, error) {
	var args updatePlanArgs
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return nil, fmt.Errorf("failed to parse function arguments: %w", err)
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return nil, fmt.Errorf("failed to parse function arguments: %w", err)
	}
	if args.Plan == nil {
		return nil, fmt.Errorf("failed to parse function arguments: missing required field plan")
	}

	plan := append([]model.PlanStep(nil), (*args.Plan)...)
	if err := validatePlanSteps(plan); err != nil {
		return nil, fmt.Errorf("failed to parse function arguments: %w", err)
	}
	return &model.PlanUpdate{
		Explanation: args.Explanation,
		Plan:        plan,
	}, nil
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var trailing struct{}
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("unexpected trailing JSON")
}

func validatePlanSteps(plan []model.PlanStep) error {
	inProgressCount := 0
	for index, step := range plan {
		if strings.TrimSpace(step.Step) == "" {
			return fmt.Errorf("plan[%d].step is required", index)
		}
		switch step.Status {
		case model.PlanStepPending, model.PlanStepCompleted:
		case model.PlanStepInProgress:
			inProgressCount++
		default:
			return fmt.Errorf("plan[%d].status %q is not supported", index, step.Status)
		}
	}
	if inProgressCount > 1 {
		return fmt.Errorf("at most one plan item can be in_progress")
	}
	return nil
}
