package model

import (
	"html"
	"regexp"
	"strings"
)

type HookPromptFragment struct {
	Text      string
	HookRunID string
}

type HookPrompt struct {
	ID        string
	Fragments []HookPromptFragment
}

var hookPromptPattern = regexp.MustCompile(`(?s)<hook_prompt\s+hook_run_id="([^"]+)">(.*?)</hook_prompt>`)

type contextualUserTag struct {
	open  string
	close string
}

// Codex registers contextual user fragments by wrapper markers and hides those
// fragments from user-visible chat history. Dexco keeps the same marker list at
// the normalized ContentPart boundary so provider adapters can evolve without
// changing this library-level visibility rule.
var contextualUserMarkedTags = []contextualUserTag{
	{open: "# AGENTS.md instructions", close: "</INSTRUCTIONS>"},
	{open: "<environment_context>", close: "</environment_context>"},
	{open: "<skill>", close: "</skill>"},
	{open: "<user_shell_command>", close: "</user_shell_command>"},
	{open: "<turn_aborted>", close: "</turn_aborted>"},
	{open: "<subagent_notification>", close: "</subagent_notification>"},
	{open: "<recommended_plugins>", close: "</recommended_plugins>"},
}

const (
	imageOpenTagText           = "<image>"
	imageCloseTagText          = "</image>"
	localImageOpenTagPrefix    = "<image name="
	localImageOpenTagSuffix    = ">"
	flattenedTextPartSeparator = "\n"
)

// ParseHookPromptParts mirrors Codex event_mapping hook-prompt handling at the
// normalized model boundary. Codex extracts visible <hook_prompt> fragments from
// otherwise contextual user messages and hides the other contextual fragments
// from the visible turn stream. Dexco keeps that behavior provider-neutral by
// parsing ContentPart text rather than raw Responses API content items.
func ParseHookPromptParts(id string, parts []ContentPart) (Item, bool) {
	fragments := make([]HookPromptFragment, 0)
	for _, part := range parts {
		if part.Kind != ContentPartText {
			continue
		}
		for _, match := range hookPromptPattern.FindAllStringSubmatch(part.Text, -1) {
			fragments = append(fragments, HookPromptFragment{
				HookRunID: match[1],
				Text:      html.UnescapeString(match[2]),
			})
		}
	}
	if len(fragments) == 0 {
		return Item{}, false
	}
	return HookPromptItem(id, fragments), true
}

func IsContextualUserPart(part ContentPart) bool {
	if part.Kind != ContentPartText {
		return false
	}
	text := strings.TrimSpace(part.Text)
	for _, tag := range contextualUserMarkedTags {
		if matchesMarkedContext(text, tag) {
			return true
		}
	}
	return matchesAdditionalContext(text) || matchesInternalModelContext(text)
}

func matchesMarkedContext(text string, tag contextualUserTag) bool {
	lower := strings.ToLower(text)
	return strings.HasPrefix(lower, strings.ToLower(tag.open)) &&
		strings.HasSuffix(lower, strings.ToLower(tag.close))
}

func matchesAdditionalContext(text string) bool {
	const openPrefix = "<external_"
	const openSuffix = ">"
	if !strings.HasPrefix(text, openPrefix) {
		return false
	}
	rest := strings.TrimPrefix(text, openPrefix)
	key, valueAndClose, ok := strings.Cut(rest, openSuffix)
	if !ok || key == "" {
		return false
	}
	return strings.HasSuffix(valueAndClose, "</external_"+key+">")
}

func matchesInternalModelContext(text string) bool {
	const (
		openPrefix  = "<codex_internal_context"
		closeMarker = "</codex_internal_context>"
		sourceStart = " source=\""
		sourceEnd   = "\">"
	)
	if matchesMarkedContext(text, contextualUserTag{open: "<goal_context>", close: "</goal_context>"}) {
		return true
	}
	if !strings.HasPrefix(text, openPrefix) {
		return false
	}
	rest := strings.TrimPrefix(text, openPrefix)
	if !strings.HasPrefix(rest, sourceStart) {
		return false
	}
	rest = strings.TrimPrefix(rest, sourceStart)
	source, bodyAndClose, ok := strings.Cut(rest, sourceEnd)
	if !ok || !isValidInternalContextSource(source) {
		return false
	}
	return strings.HasSuffix(bodyAndClose, closeMarker)
}

func isValidInternalContextSource(source string) bool {
	for idx, r := range source {
		if idx == 0 {
			if r < 'a' || r > 'z' {
				return false
			}
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return source != ""
}

func VisibleUserInputItem(id string, content string, parts []ContentPart) (Item, bool) {
	rawParts := parts
	if len(rawParts) == 0 && content != "" {
		rawParts = []ContentPart{{Kind: ContentPartText, Text: content}}
	}

	if item, ok := ParseHookPromptParts(id, rawParts); ok {
		return item, true
	}
	for _, part := range rawParts {
		if IsContextualUserPart(part) {
			return Item{}, false
		}
	}
	if len(parts) == 0 {
		return UserInputItem(content), true
	}

	// Codex event_mapping.rs treats ContentItem::InputText values that wrap an
	// adjacent InputImage as UI/transport scaffolding, not user-authored text:
	// local images are serialized as `<image name=...>`, image data, `</image>`,
	// while unnamed image placeholders use `<image>`, image data, `</image>`.
	// Dexco normalizes the same pattern at the provider-neutral ContentPart
	// boundary so future Responses/OpenAI adapter changes only need to update
	// this shim. The adjacency requirement is important: a literal user message
	// containing "<image>" must remain visible unless it actually labels an
	// image part.
	visibleParts := normalizeVisibleUserParts(parts)
	return UserInputItemWithParts(flattenTextParts(visibleParts), visibleParts), true
}

func HookPromptItem(id string, fragments []HookPromptFragment) Item {
	copied := append([]HookPromptFragment(nil), fragments...)
	prompt := HookPrompt{
		ID:        id,
		Fragments: copied,
	}
	return Item{
		Kind:       ItemHookPrompt,
		HookPrompt: &prompt,
	}
}

func normalizeVisibleUserParts(parts []ContentPart) []ContentPart {
	visibleParts := make([]ContentPart, 0, len(parts))
	for idx, part := range parts {
		if part.Kind == ContentPartText && isSyntheticImageLabelText(parts, idx, part.Text) {
			continue
		}
		visibleParts = append(visibleParts, part)
	}
	return visibleParts
}

func isSyntheticImageLabelText(parts []ContentPart, idx int, text string) bool {
	if (isLocalImageOpenTagText(text) || isImageOpenTagText(text)) &&
		idx+1 < len(parts) &&
		parts[idx+1].Kind == ContentPartImage {
		return true
	}
	if idx > 0 &&
		(isLocalImageCloseTagText(text) || isImageCloseTagText(text)) &&
		parts[idx-1].Kind == ContentPartImage {
		return true
	}
	return false
}

func isLocalImageOpenTagText(text string) bool {
	return strings.HasPrefix(text, localImageOpenTagPrefix) &&
		strings.HasSuffix(text, localImageOpenTagSuffix)
}

func isLocalImageCloseTagText(text string) bool {
	return isImageCloseTagText(text)
}

func isImageOpenTagText(text string) bool {
	return text == imageOpenTagText
}

func isImageCloseTagText(text string) bool {
	return text == imageCloseTagText
}

func flattenTextParts(parts []ContentPart) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Kind == ContentPartText && part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, flattenedTextPartSeparator)
}
