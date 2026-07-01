package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openai/codex/dexco/internal/model"
	permissionstore "github.com/openai/codex/dexco/internal/permissions"
)

type PermissionGrantResponder func(
	ctx context.Context,
	request RequestPermissionsArgs,
) (RequestPermissionsResponse, error)

type RequestPermissionsHandler struct {
	Responder PermissionGrantResponder
	Grants    *permissionstore.Store
}

type RequestPermissionsArgs struct {
	Reason string                  `json:"reason,omitempty"`
	Grants []model.PermissionGrant `json:"grants"`
}

type RequestPermissionsResponse struct {
	Grants           []model.PermissionGrant    `json:"grants"`
	Scope            model.PermissionGrantScope `json:"scope"`
	StrictAutoReview bool                       `json:"strictAutoReview,omitempty"`
}

func (RequestPermissionsHandler) Name() string {
	return "request_permissions"
}

func (RequestPermissionsHandler) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name: "request_permissions",
		Description: "Request additional permission grant keys from the host application. " +
			"Granted keys apply to later guarded tools in this turn, or across turns if approved for session scope.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reason": map[string]any{
					"type":        "string",
					"description": "Optional short explanation for why additional permissions are needed.",
				},
				"grants": map[string]any{
					"type":        "array",
					"description": "Permission grant keys requested by later tool guardrails.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"key": map[string]any{
								"type":        "string",
								"description": "Stable key a later tool guardrail will require.",
							},
							"description": map[string]any{
								"type":        "string",
								"description": "Optional user-facing description of the grant.",
							},
						},
						"required": []string{"key"},
					},
				},
			},
			"required": []string{"grants"},
		},
	}
}

func (RequestPermissionsHandler) Guardrail(context.Context, model.ToolCall) (model.ToolGuardrail, error) {
	return model.ToolGuardrail{
		Risk:                model.ToolRiskUserInteraction,
		ApprovalRequirement: model.ApprovalRequirementNone,
		Reason:              "requests permission grants from the host application",
	}, nil
}

func (h RequestPermissionsHandler) Call(ctx context.Context, call model.ToolCall) (model.ToolResult, error) {
	var args RequestPermissionsArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return model.ToolResult{}, fmt.Errorf("parse request_permissions arguments: %w", err)
	}
	if err := validatePermissionGrants(args.Grants); err != nil {
		return model.ToolResult{}, err
	}

	response := RequestPermissionsResponse{
		Scope: model.PermissionGrantScopeTurn,
	}
	if h.Responder != nil {
		granted, err := h.Responder(ctx, args)
		if err != nil {
			return model.ToolResult{}, fmt.Errorf("request permissions: %w", err)
		}
		response = granted
	}
	response = normalizeRequestPermissionsResponse(args.Grants, response)

	for _, grant := range response.Grants {
		if err := h.Grants.Record(
			ctx,
			permissionstore.TurnIDFromContext(ctx),
			grant,
			response.Scope,
			response.StrictAutoReview,
		); err != nil {
			return model.ToolResult{}, err
		}
	}

	output, err := json.Marshal(response)
	if err != nil {
		return model.ToolResult{}, fmt.Errorf("serialize request_permissions response: %w", err)
	}
	return model.ToolResult{
		CallID:  call.CallID,
		Name:    call.Name,
		Output:  string(output),
		Success: true,
	}, nil
}

func validatePermissionGrants(grants []model.PermissionGrant) error {
	if len(grants) == 0 {
		return fmt.Errorf("request_permissions requires at least one grant")
	}
	for _, grant := range grants {
		if grant.Key == "" {
			return fmt.Errorf("request_permissions grant requires non-empty key")
		}
	}
	return nil
}

func normalizeRequestPermissionsResponse(
	requested []model.PermissionGrant,
	response RequestPermissionsResponse,
) RequestPermissionsResponse {
	if response.Scope == "" {
		response.Scope = model.PermissionGrantScopeTurn
	}
	if response.StrictAutoReview && response.Scope == model.PermissionGrantScopeSession {
		// Codex rejects strict auto-review at session scope because it is a
		// guardian-for-this-turn behavior, not a persistent permission grant.
		return RequestPermissionsResponse{
			Grants:           nil,
			Scope:            model.PermissionGrantScopeTurn,
			StrictAutoReview: false,
		}
	}

	granted := make(map[string]struct{}, len(response.Grants))
	for _, grant := range response.Grants {
		if grant.Key != "" {
			granted[grant.Key] = struct{}{}
		}
	}

	normalized := response
	normalized.Grants = normalized.Grants[:0]
	for _, grant := range requested {
		if _, ok := granted[grant.Key]; ok {
			normalized.Grants = append(normalized.Grants, grant)
		}
	}
	return normalized
}
