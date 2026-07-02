package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rizrmd/dexco/internal/model"
)

const (
	minAutoResolutionMS = 60000
	maxAutoResolutionMS = 240000
)

type UserInputResponder func(ctx context.Context, prompt string) (string, error)

type StructuredUserInputResponder func(
	ctx context.Context,
	request RequestUserInputArgs,
) (RequestUserInputResponse, error)

type RequestUserInputHandler struct {
	Responder           UserInputResponder
	StructuredResponder StructuredUserInputResponder
}

type RequestUserInputOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type RequestUserInputQuestion struct {
	ID       string `json:"id"`
	Header   string `json:"header"`
	Question string `json:"question"`
	// IsOther and IsSecret are Codex client-facing question metadata fields.
	// The model-facing tool schema intentionally does not ask the model to set
	// IsOther. Codex normalizes every accepted structured question to
	// isOther=true so clients can append their implicit free-form "Other"
	// answer without relying on model cooperation. Dexco mirrors that boundary
	// behavior here to keep library integrations compatible with Codex clients.
	IsOther  bool                     `json:"isOther,omitempty"`
	IsSecret bool                     `json:"isSecret,omitempty"`
	Options  []RequestUserInputOption `json:"options"`
}

type RequestUserInputArgs struct {
	// Question is Dexco's original compact form: one prompt, one plain answer.
	Question string `json:"question"`
	// Questions mirrors Codex's richer request_user_input payload. Dexco keeps
	// this shape so future Codex improvements to question metadata can be
	// adopted without changing the tool loop contract again.
	Questions        []RequestUserInputQuestion `json:"questions"`
	AutoResolutionMS *int                       `json:"autoResolutionMs,omitempty"`
}

type RequestUserInputAnswer struct {
	Answers []string `json:"answers"`
}

type RequestUserInputResponse struct {
	Answers map[string]RequestUserInputAnswer `json:"answers"`
}

func (RequestUserInputHandler) Name() string {
	return "request_user_input"
}

func (RequestUserInputHandler) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        "request_user_input",
		Description: "Requests a short answer from the user and returns it to the model.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "Question to ask the user.",
				},
				"questions": map[string]any{
					"type":        "array",
					"description": "Codex-style structured questions with IDs and options.",
				},
				"autoResolutionMs": map[string]any{
					"type":        "number",
					"description": "Optional Codex auto-resolution timeout in milliseconds.",
				},
			},
		},
	}
}

func (RequestUserInputHandler) Guardrail(context.Context, model.ToolCall) (model.ToolGuardrail, error) {
	return model.ToolGuardrail{
		Risk:                model.ToolRiskUserInteraction,
		ApprovalRequirement: model.ApprovalRequirementNone,
		Reason:              "asks the user for input",
	}, nil
}

func (h RequestUserInputHandler) Call(ctx context.Context, call model.ToolCall) (model.ToolResult, error) {
	if h.Responder == nil && h.StructuredResponder == nil {
		return model.ToolResult{}, fmt.Errorf("request_user_input has no responder configured")
	}

	var args RequestUserInputArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return model.ToolResult{}, fmt.Errorf("parse request_user_input arguments: %w", err)
	}

	if len(args.Questions) > 0 {
		if err := normalizeStructuredRequestUserInputArgs(&args); err != nil {
			return model.ToolResult{}, err
		}
		return h.callStructured(ctx, call, args)
	}

	if strings.TrimSpace(args.Question) == "" {
		return model.ToolResult{}, fmt.Errorf("request_user_input requires non-empty question")
	}
	if h.Responder == nil {
		return model.ToolResult{}, fmt.Errorf("request_user_input has no simple responder configured")
	}

	answer, err := h.Responder(ctx, args.Question)
	if err != nil {
		return model.ToolResult{}, fmt.Errorf("request user input: %w", err)
	}

	return model.ToolResult{
		CallID:  call.CallID,
		Name:    call.Name,
		Output:  answer,
		Success: true,
	}, nil
}

func normalizeStructuredRequestUserInputArgs(args *RequestUserInputArgs) error {
	if args == nil {
		return nil
	}
	for i := range args.Questions {
		if len(args.Questions[i].Options) == 0 {
			return fmt.Errorf("request_user_input requires non-empty options for every question")
		}
		// Codex appends the client-owned free-form Other option after validating
		// that the model supplied real mutually-exclusive options. Dexco sets
		// the same flag before handing the request to a structured responder, so
		// downstream clients do not need to duplicate Codex normalization rules.
		args.Questions[i].IsOther = true
	}

	if args.AutoResolutionMS == nil {
		return nil
	}
	value := *args.AutoResolutionMS
	if value < minAutoResolutionMS {
		value = minAutoResolutionMS
	}
	if value > maxAutoResolutionMS {
		value = maxAutoResolutionMS
	}
	args.AutoResolutionMS = &value
	return nil
}

func (h RequestUserInputHandler) callStructured(
	ctx context.Context,
	call model.ToolCall,
	args RequestUserInputArgs,
) (model.ToolResult, error) {
	if h.StructuredResponder != nil {
		response, err := h.StructuredResponder(ctx, args)
		if err != nil {
			return model.ToolResult{}, fmt.Errorf("request structured user input: %w", err)
		}
		return structuredRequestUserInputResult(call, response)
	}
	if h.Responder == nil {
		return model.ToolResult{}, fmt.Errorf("request_user_input has no simple responder configured")
	}

	question := args.Questions[0]
	if strings.TrimSpace(question.ID) == "" {
		return model.ToolResult{}, fmt.Errorf("request_user_input structured question requires non-empty id")
	}
	if strings.TrimSpace(question.Question) == "" {
		return model.ToolResult{}, fmt.Errorf("request_user_input structured question requires non-empty question")
	}

	answer, err := h.Responder(ctx, question.Question)
	if err != nil {
		return model.ToolResult{}, fmt.Errorf("request user input: %w", err)
	}
	return structuredRequestUserInputResult(call, RequestUserInputResponse{
		Answers: map[string]RequestUserInputAnswer{
			question.ID: {Answers: []string{answer}},
		},
	})
}

func structuredRequestUserInputResult(
	call model.ToolCall,
	response RequestUserInputResponse,
) (model.ToolResult, error) {
	if response.Answers == nil {
		response.Answers = map[string]RequestUserInputAnswer{}
	}
	output, err := json.Marshal(response)
	if err != nil {
		return model.ToolResult{}, fmt.Errorf("serialize request_user_input response: %w", err)
	}
	return model.ToolResult{
		CallID:  call.CallID,
		Name:    call.Name,
		Output:  string(output),
		Success: true,
	}, nil
}
