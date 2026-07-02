package builtin

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/rizrmd/dexco/internal/model"
)

func TestSleepHandlerSpecGuardrailAndInterruptOptIn(t *testing.T) {
	t.Parallel()

	handler := SleepHandler{}
	spec := handler.Spec()
	if spec.Name != "sleep" {
		t.Fatalf("Name = %q, want sleep", spec.Name)
	}
	required, ok := spec.Parameters["required"].([]string)
	if !ok || !reflect.DeepEqual(required, []string{"duration_ms"}) {
		t.Fatalf("required = %#v, want duration_ms", spec.Parameters["required"])
	}
	if !handler.InterruptsOnPendingInput() {
		t.Fatalf("InterruptsOnPendingInput() = false, want true")
	}

	got, err := handler.Guardrail(context.Background(), model.ToolCall{Name: "sleep"})
	if err != nil {
		t.Fatalf("Guardrail() error = %v", err)
	}
	want := model.ToolGuardrail{
		Risk:                model.ToolRiskReadOnly,
		ApprovalRequirement: model.ApprovalRequirementNone,
		Reason:              "pause until a timer or pending input fires",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Guardrail() = %#v, want %#v", got, want)
	}
}

func TestSleepHandlerCompletesAndRecordsWallTime(t *testing.T) {
	t.Parallel()

	result, err := SleepHandler{}.Call(context.Background(), model.ToolCall{
		CallID:    "sleep-call",
		Name:      "sleep",
		Arguments: json.RawMessage(`{"duration_ms":1}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !result.Success {
		t.Fatalf("Success = false, want true")
	}
	wallTime := sleepOutputWallTime(t, result.Output, "Sleep completed.")
	if wallTime <= 0 {
		t.Fatalf("wall time = %f, want positive", wallTime)
	}
}

func TestSleepHandlerInterruptsOnContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := SleepHandler{}.Call(ctx, model.ToolCall{
		CallID:    "sleep-call",
		Name:      "sleep",
		Arguments: json.RawMessage(`{"duration_ms":1000}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !result.Success {
		t.Fatalf("Success = false, want true")
	}
	_ = sleepOutputWallTime(t, result.Output, "Sleep interrupted by new input.")
}

func TestSleepHandlerValidatesDuration(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		arguments string
		want      string
	}{
		{name: "zero", arguments: `{"duration_ms":0}`, want: "duration_ms must be between"},
		{name: "too large", arguments: `{"duration_ms":43200001}`, want: "duration_ms must be between"},
		{name: "unknown field", arguments: `{"duration_ms":1,"extra":true}`, want: "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := SleepHandler{}.Call(context.Background(), model.ToolCall{
				CallID:    tc.name,
				Name:      "sleep",
				Arguments: json.RawMessage(tc.arguments),
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Call() error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func sleepOutputWallTime(t *testing.T, output string, message string) float64 {
	t.Helper()
	prefix := "Wall time: "
	suffix := " seconds\n" + message
	raw, ok := strings.CutPrefix(output, prefix)
	if !ok {
		t.Fatalf("Output = %q, want prefix %q", output, prefix)
	}
	raw, ok = strings.CutSuffix(raw, suffix)
	if !ok {
		t.Fatalf("Output = %q, want suffix %q", output, suffix)
	}
	wallTime, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		t.Fatalf("parse wall time %q: %v", raw, err)
	}
	return wallTime
}
