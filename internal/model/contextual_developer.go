package model

import "strings"

var contextualDeveloperPrefixes = []string{
	"<permissions instructions>",
	"<model_switch>",
	"<collaboration_mode>",
	"<multi_agent_mode>",
	"<realtime_conversation>",
	"<skills_instructions>",
	"<personality_spec>",
	// Codex still recognizes token-budget wrappers persisted by older versions.
	"<token_budget>",
	"<context_window>",
	"<context_window_guidance>",
	"<rollout_budget>",
}

// IsContextualDeveloperPart mirrors Codex event_mapping's
// is_contextual_dev_fragment predicate. These fragments are model-visible
// developer context, but they are rollback-trimmable/contextual rather than
// stable user-authored developer instructions.
func IsContextualDeveloperPart(part ContentPart) bool {
	if part.Kind != ContentPartText {
		return false
	}
	trimmed := strings.TrimLeft(part.Text, " \t\r\n")
	lower := strings.ToLower(trimmed)
	for _, prefix := range contextualDeveloperPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func IsContextualDeveloperContent(parts []ContentPart) bool {
	for _, part := range parts {
		if IsContextualDeveloperPart(part) {
			return true
		}
	}
	return false
}

// HasNonContextualDeveloperContent is the companion to
// IsContextualDeveloperContent, matching Codex's handling for mixed developer
// messages: a message can contain contextual fragments and persistent developer
// instructions at the same time, and callers may need to distinguish the two.
func HasNonContextualDeveloperContent(parts []ContentPart) bool {
	for _, part := range parts {
		if !IsContextualDeveloperPart(part) {
			return true
		}
	}
	return false
}
