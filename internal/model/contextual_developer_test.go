package model

import "testing"

// Adapted from Codex event_mapping contextual developer tests. Dexco keeps
// developer prompt fragments provider-neutral, but the same classification is
// needed so integrations can distinguish rollback-trimmable context from stable
// developer instructions.
func TestContextualDeveloperContentRecognizesCodexPrefixes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
	}{
		{name: "skills", text: "<skills_instructions>\n## Skills"},
		{name: "legacy token budget", text: "<token_budget>\nYou have 710 tokens left.\n</token_budget>"},
		{name: "context window", text: "<context_window>\nThread id: 00000000-0000-0000-0000-000000000000\n</context_window>"},
		{name: "context window guidance", text: "<context_window_guidance>\nPreserve important state.\n</context_window_guidance>"},
		{name: "permissions", text: "   <permissions instructions>\nFilesystem sandboxing defines which files can be read."},
		{name: "mixed case", text: "\n<SKILLS_INSTRUCTIONS>\n## Skills"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parts := []ContentPart{{Kind: ContentPartText, Text: tt.text}}
			if !IsContextualDeveloperContent(parts) {
				t.Fatalf("IsContextualDeveloperContent(%q) = false, want true", tt.text)
			}
			if HasNonContextualDeveloperContent(parts) {
				t.Fatalf("HasNonContextualDeveloperContent(%q) = true, want false", tt.text)
			}
		})
	}
}

func TestContextualDeveloperContentDetectsMixedPersistentDeveloperText(t *testing.T) {
	t.Parallel()

	parts := []ContentPart{
		{Kind: ContentPartText, Text: "<context_window>\nThread id: abc\n</context_window>"},
		{Kind: ContentPartText, Text: "Always answer in terse prose."},
	}

	if !IsContextualDeveloperContent(parts) {
		t.Fatalf("IsContextualDeveloperContent() = false, want true")
	}
	if !HasNonContextualDeveloperContent(parts) {
		t.Fatalf("HasNonContextualDeveloperContent() = false, want true")
	}
}

func TestContextualDeveloperContentIgnoresNonTextParts(t *testing.T) {
	t.Parallel()

	parts := []ContentPart{{Kind: ContentPartImage, ImageURL: "data:image/png;base64,abc"}}
	if IsContextualDeveloperContent(parts) {
		t.Fatalf("IsContextualDeveloperContent() = true for image-only content")
	}
	if !HasNonContextualDeveloperContent(parts) {
		t.Fatalf("HasNonContextualDeveloperContent() = false for image-only content")
	}
}
