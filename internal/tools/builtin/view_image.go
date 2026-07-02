package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rizrmd/dexco/internal/imageprep"
	"github.com/rizrmd/dexco/internal/model"
)

const (
	defaultViewImageDetail = "high"
	originalImageDetail    = "original"
)

type ViewImageHandler struct {
	BaseDir string
}

type ViewImageArgs struct {
	Path   string  `json:"path"`
	Detail *string `json:"detail,omitempty"`
}

func (ViewImageHandler) Name() string {
	return "view_image"
}

func (ViewImageHandler) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        "view_image",
		Description: "Attach a local image to the model-visible tool result.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the local image.",
				},
				"detail": map[string]any{
					"type":        "string",
					"description": "Optional detail level: high or original.",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (h ViewImageHandler) Guardrail(context.Context, model.ToolCall) (model.ToolGuardrail, error) {
	return model.ToolGuardrail{
		Risk:                model.ToolRiskReadOnly,
		ApprovalRequirement: model.ApprovalRequirementNone,
		Reason:              "read-only image attachment",
	}, nil
}

func (h ViewImageHandler) Call(_ context.Context, call model.ToolCall) (model.ToolResult, error) {
	var args ViewImageArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return model.ToolResult{}, fmt.Errorf("parse view_image arguments: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return model.ToolResult{}, fmt.Errorf("view_image requires non-empty path")
	}

	detail := defaultViewImageDetail
	if args.Detail != nil {
		detail = strings.TrimSpace(*args.Detail)
	}
	if detail == "" {
		detail = defaultViewImageDetail
	}
	if detail != defaultViewImageDetail && detail != originalImageDetail {
		return model.ToolResult{
			CallID:  call.CallID,
			Name:    call.Name,
			Output:  fmt.Sprintf("view_image.detail only supports `high` or `original`; omit `detail` for default high resized behavior, got `%s`", detail),
			Success: false,
		}, nil
	}

	path := args.Path
	if !filepath.IsAbs(path) {
		baseDir := h.BaseDir
		if baseDir == "" {
			baseDir = "."
		}
		path = filepath.Join(baseDir, path)
	}
	budget := imageprep.NoBudget
	if detail == defaultViewImageDetail {
		budget = imageprep.HighBudget
	}
	part, err := imageprep.PrepareLocalImage(path, detail, budget)
	if err != nil {
		return model.ToolResult{}, fmt.Errorf("prepare view_image image: %w", err)
	}

	// Codex view_image outputs an image content item rather than prose. Dexco
	// mirrors that shape in ToolResult.Parts while leaving Output empty so model
	// clients do not need to scrape text to discover attached images.
	return model.ToolResult{
		CallID: call.CallID,
		Name:   call.Name,
		Parts: []model.ContentPart{
			part,
		},
		Success: true,
	}, nil
}
