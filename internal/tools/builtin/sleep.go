package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rizrmd/dexco/internal/model"
)

const MaxSleepDurationMS int64 = 12 * 60 * 60 * 1000

type SleepHandler struct{}

type SleepArgs struct {
	DurationMS int64 `json:"duration_ms"`
}

func (SleepHandler) Name() string {
	return "sleep"
}

func (SleepHandler) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        "sleep",
		Description: "Pause execution for a specified duration. The sleep ends early when new input arrives for the active turn. Returns the elapsed wall-clock time.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"duration_ms": map[string]any{
					"type":        "number",
					"description": fmt.Sprintf("How long to sleep in milliseconds. Must be between 1 and %d.", MaxSleepDurationMS),
				},
			},
			"required": []string{"duration_ms"},
		},
	}
}

func (SleepHandler) Guardrail(context.Context, model.ToolCall) (model.ToolGuardrail, error) {
	return model.ToolGuardrail{
		Risk:                model.ToolRiskReadOnly,
		ApprovalRequirement: model.ApprovalRequirementNone,
		Reason:              "pause until a timer or pending input fires",
	}, nil
}

func (SleepHandler) InterruptsOnPendingInput() bool {
	return true
}

func (SleepHandler) Call(ctx context.Context, call model.ToolCall) (model.ToolResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	var args SleepArgs
	if err := decoder.Decode(&args); err != nil {
		return model.ToolResult{}, fmt.Errorf("parse sleep arguments: %w", err)
	}
	if args.DurationMS < 1 || args.DurationMS > MaxSleepDurationMS {
		return model.ToolResult{}, fmt.Errorf("duration_ms must be between 1 and %d", MaxSleepDurationMS)
	}

	started := time.Now()
	timer := time.NewTimer(time.Duration(args.DurationMS) * time.Millisecond)
	defer timer.Stop()

	message := "Sleep completed."
	select {
	case <-timer.C:
	case <-ctx.Done():
		// Codex's sleep tool returns a normal model-visible result for pending
		// input interruption. The runner still aborts on true turn cancellation
		// because only the tool context, not the parent turn context, is canceled
		// for pending-input wakeups.
		message = "Sleep interrupted by new input."
	}

	return model.ToolResult{
		Output:  fmt.Sprintf("Wall time: %.4f seconds\n%s", time.Since(started).Seconds(), message),
		Success: true,
	}, nil
}
