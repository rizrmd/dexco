package builtin

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/openai/codex/dexco/internal/model"
)

func TestRequestUserInputHandlerUsesResponder(t *testing.T) {
	t.Parallel()

	var prompted string
	handler := RequestUserInputHandler{
		Responder: func(_ context.Context, prompt string) (string, error) {
			prompted = prompt
			return "42", nil
		},
	}

	result, err := handler.Call(context.Background(), model.ToolCall{
		CallID:    "call-1",
		Name:      "request_user_input",
		Arguments: json.RawMessage(`{"question":"What is the answer?"}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	if prompted != "What is the answer?" {
		t.Fatalf("prompted = %q, want %q", prompted, "What is the answer?")
	}
	if result.Output != "42" {
		t.Fatalf("Output = %q, want %q", result.Output, "42")
	}
	if !result.Success {
		t.Fatalf("Success = false, want true")
	}
}

// Adapted from Codex request_user_input spec tests. Dexco's schema is smaller
// than Codex's exact JSON schema object, but it must advertise the same
// structured entry points so a Codex-style model tool call is discoverable.
func TestRequestUserInputHandlerSpecIncludesStructuredQuestionFields(t *testing.T) {
	t.Parallel()

	spec := RequestUserInputHandler{}.Spec()
	properties, ok := spec.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v, want map", spec.Parameters["properties"])
	}
	for _, field := range []string{"questions", "autoResolutionMs"} {
		if _, ok := properties[field]; !ok {
			t.Fatalf("properties missing %q: %#v", field, properties)
		}
	}
}

// Adapted from Codex core's request_user_input structured payload tests. Codex
// sends a list of question objects and expects a JSON answer map keyed by
// question ID. Dexco keeps the older one-prompt responder for simple library
// callers, but still returns the Codex-compatible answer envelope when the
// model uses the richer payload shape.
func TestRequestUserInputHandlerUsesCodexStructuredQuestionsWithSimpleResponder(t *testing.T) {
	t.Parallel()

	var prompted string
	handler := RequestUserInputHandler{
		Responder: func(_ context.Context, prompt string) (string, error) {
			prompted = prompt
			return "yes", nil
		},
	}

	result, err := handler.Call(context.Background(), model.ToolCall{
		CallID: "structured-call",
		Name:   "request_user_input",
		Arguments: json.RawMessage(`{
			"questions": [{
				"id": "confirm_path",
				"header": "Confirm",
				"question": "Proceed with the plan?",
				"options": [{
					"label": "Yes (Recommended)",
					"description": "Continue the current plan."
				}, {
					"label": "No",
					"description": "Stop and revisit the approach."
				}]
			}],
			"autoResolutionMs": 60000
		}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	if prompted != "Proceed with the plan?" {
		t.Fatalf("prompted = %q, want Proceed with the plan?", prompted)
	}
	var response RequestUserInputResponse
	if err := json.Unmarshal([]byte(result.Output), &response); err != nil {
		t.Fatalf("structured output is not JSON: %v", err)
	}
	want := RequestUserInputResponse{
		Answers: map[string]RequestUserInputAnswer{
			"confirm_path": {Answers: []string{"yes"}},
		},
	}
	if !reflect.DeepEqual(response, want) {
		t.Fatalf("response = %#v, want %#v", response, want)
	}
	if !result.Success {
		t.Fatalf("Success = false, want true")
	}
}

func TestRequestUserInputHandlerUsesStructuredResponder(t *testing.T) {
	t.Parallel()

	var captured RequestUserInputArgs
	handler := RequestUserInputHandler{
		StructuredResponder: func(_ context.Context, request RequestUserInputArgs) (RequestUserInputResponse, error) {
			captured = request
			return RequestUserInputResponse{
				Answers: map[string]RequestUserInputAnswer{
					"choice": {Answers: []string{"A", "B"}},
				},
			}, nil
		},
	}

	result, err := handler.Call(context.Background(), model.ToolCall{
		CallID: "structured-call",
		Name:   "request_user_input",
		Arguments: json.RawMessage(`{
			"questions": [{
				"id": "choice",
				"header": "Choose",
				"question": "Pick options",
				"options": [{
					"label": "A",
					"description": "First"
				}]
			}],
			"autoResolutionMs": 120000
		}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	if captured.AutoResolutionMS == nil || *captured.AutoResolutionMS != 120000 {
		t.Fatalf("AutoResolutionMS = %#v, want 120000", captured.AutoResolutionMS)
	}
	wantAutoResolutionMS := 120000
	wantRequest := RequestUserInputArgs{
		Questions: []RequestUserInputQuestion{{
			ID:       "choice",
			Header:   "Choose",
			Question: "Pick options",
			IsOther:  true,
			Options: []RequestUserInputOption{{
				Label:       "A",
				Description: "First",
			}},
		}},
		AutoResolutionMS: &wantAutoResolutionMS,
	}
	if !reflect.DeepEqual(captured, wantRequest) {
		t.Fatalf("captured request = %#v, want %#v", captured, wantRequest)
	}
	var response RequestUserInputResponse
	if err := json.Unmarshal([]byte(result.Output), &response); err != nil {
		t.Fatalf("structured output is not JSON: %v", err)
	}
	want := RequestUserInputResponse{
		Answers: map[string]RequestUserInputAnswer{
			"choice": {Answers: []string{"A", "B"}},
		},
	}
	if !reflect.DeepEqual(response, want) {
		t.Fatalf("response = %#v, want %#v", response, want)
	}
}

// Adapted from Codex's request_user_input normalization tests. Dexco does not
// own Codex's UI auto-resolution behavior, but the structured responder should
// see the same bounded value when models send an out-of-range hint.
func TestRequestUserInputHandlerNormalizesAutoResolutionMSForStructuredResponder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want int
	}{
		{name: "below minimum", raw: "1", want: 60000},
		{name: "above maximum", raw: "999999", want: 240000},
		{name: "within range", raw: "120000", want: 120000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var captured RequestUserInputArgs
			handler := RequestUserInputHandler{
				StructuredResponder: func(_ context.Context, request RequestUserInputArgs) (RequestUserInputResponse, error) {
					captured = request
					return RequestUserInputResponse{}, nil
				},
			}
			_, err := handler.Call(context.Background(), model.ToolCall{
				CallID: "structured-call",
				Name:   "request_user_input",
				Arguments: json.RawMessage(`{
					"questions": [{
						"id": "confirm",
						"header": "Confirm",
						"question": "Proceed?",
						"options": [{
							"label": "Yes (Recommended)",
							"description": "Continue."
						}]
					}],
					"autoResolutionMs": ` + tt.raw + `
				}`),
			})
			if err != nil {
				t.Fatalf("Call() error = %v", err)
			}

			if captured.AutoResolutionMS == nil || *captured.AutoResolutionMS != tt.want {
				t.Fatalf("AutoResolutionMS = %#v, want %d", captured.AutoResolutionMS, tt.want)
			}
		})
	}
}

// Codex intentionally keeps the implicit free-form "Other" option out of the
// model-facing schema. The normalizer owns that client contract, so Dexco's
// structured responder sees isOther=true even when the model omits or clears
// the field, while other client metadata such as isSecret is preserved.
func TestRequestUserInputHandlerEnablesImplicitOtherOptionAndPreservesSecret(t *testing.T) {
	t.Parallel()

	var captured RequestUserInputArgs
	handler := RequestUserInputHandler{
		StructuredResponder: func(_ context.Context, request RequestUserInputArgs) (RequestUserInputResponse, error) {
			captured = request
			return RequestUserInputResponse{}, nil
		},
	}

	_, err := handler.Call(context.Background(), model.ToolCall{
		CallID: "structured-call",
		Name:   "request_user_input",
		Arguments: json.RawMessage(`{
			"questions": [{
				"id": "confirm",
				"header": "Confirm",
				"question": "Proceed?",
				"isOther": false,
				"isSecret": true,
				"options": [{
					"label": "Yes (Recommended)",
					"description": "Continue."
				}]
			}]
		}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	want := RequestUserInputArgs{
		Questions: []RequestUserInputQuestion{{
			ID:       "confirm",
			Header:   "Confirm",
			Question: "Proceed?",
			IsOther:  true,
			IsSecret: true,
			Options: []RequestUserInputOption{{
				Label:       "Yes (Recommended)",
				Description: "Continue.",
			}},
		}},
	}
	if !reflect.DeepEqual(captured, want) {
		t.Fatalf("captured request = %#v, want %#v", captured, want)
	}
}

// Adapted from Codex's missing-options guard. The structured question payload
// must contain real choices because the client adds its own Other choice; an
// empty options list would otherwise produce a UI with only client-synthesized
// input and diverge from Codex's model contract.
func TestRequestUserInputHandlerRejectsStructuredQuestionWithoutOptions(t *testing.T) {
	t.Parallel()

	handler := RequestUserInputHandler{
		StructuredResponder: func(context.Context, RequestUserInputArgs) (RequestUserInputResponse, error) {
			t.Fatalf("StructuredResponder should not be called for invalid structured input")
			return RequestUserInputResponse{}, nil
		},
	}

	_, err := handler.Call(context.Background(), model.ToolCall{
		CallID: "structured-call",
		Name:   "request_user_input",
		Arguments: json.RawMessage(`{
			"questions": [{
				"id": "confirm",
				"header": "Confirm",
				"question": "Proceed?",
				"options": []
			}]
		}`),
	})
	if err == nil {
		t.Fatalf("Call() error = nil, want missing options error")
	}
	if err.Error() != "request_user_input requires non-empty options for every question" {
		t.Fatalf("Call() error = %q", err)
	}
}
