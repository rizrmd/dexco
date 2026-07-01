package model

// ContentPartsWithoutImageDetail returns a copy of parts with image Detail
// cleared. Codex uses this when building Responses Lite request copies because
// that transport does not accept per-image detail, while the stored prompt
// history must keep the original image metadata intact.
func ContentPartsWithoutImageDetail(parts []ContentPart) []ContentPart {
	if len(parts) == 0 {
		return nil
	}
	stripped := make([]ContentPart, 0, len(parts))
	for _, part := range parts {
		if part.Kind == ContentPartImage {
			part.Detail = ""
		}
		stripped = append(stripped, part)
	}
	return stripped
}

// ItemsWithoutImageDetail adapts Codex's
// responses_lite_request_copies_strip_image_details behavior to Dexco's
// provider-neutral Item model. It strips details from user/image parts and rich
// tool-result parts in the returned copy only.
func ItemsWithoutImageDetail(items []Item) []Item {
	if len(items) == 0 {
		return nil
	}
	copied := make([]Item, 0, len(items))
	for _, item := range items {
		item.Parts = ContentPartsWithoutImageDetail(item.Parts)
		if item.ToolCall != nil {
			toolCall := *item.ToolCall
			toolCall.Arguments = append([]byte(nil), toolCall.Arguments...)
			item.ToolCall = &toolCall
		}
		if item.ToolResult != nil {
			toolResult := *item.ToolResult
			toolResult.Parts = ContentPartsWithoutImageDetail(toolResult.Parts)
			if toolResult.PlanUpdate != nil {
				planUpdate := *toolResult.PlanUpdate
				planUpdate.Plan = append([]PlanStep(nil), planUpdate.Plan...)
				toolResult.PlanUpdate = &planUpdate
			}
			item.ToolResult = &toolResult
		}
		if item.WebSearch != nil {
			webSearch := *item.WebSearch
			webSearch.Action.Queries = append([]string(nil), webSearch.Action.Queries...)
			item.WebSearch = &webSearch
		}
		if item.HookPrompt != nil {
			hookPrompt := *item.HookPrompt
			hookPrompt.Fragments = append([]HookPromptFragment(nil), hookPrompt.Fragments...)
			item.HookPrompt = &hookPrompt
		}
		if item.ImageGeneration != nil {
			imageGeneration := *item.ImageGeneration
			item.ImageGeneration = &imageGeneration
		}
		copied = append(copied, item)
	}
	return copied
}
