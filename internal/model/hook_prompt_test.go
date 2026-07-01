package model

import (
	"reflect"
	"testing"
)

// Adapted from Codex event_mapping hook prompt tests. Hook prompts are visible
// turn items, while neighboring contextual fragments such as environment context
// remain hidden from visible user-message history.
func TestParseHookPromptPartsHidesOtherContextualFragments(t *testing.T) {
	t.Parallel()

	item, ok := ParseHookPromptParts("msg-1", []ContentPart{
		{Kind: ContentPartText, Text: "<environment_context>ctx</environment_context>"},
		{Kind: ContentPartText, Text: `<hook_prompt hook_run_id="hook-run-1">Retry with care &amp; joy.</hook_prompt>`},
	})
	if !ok {
		t.Fatalf("ParseHookPromptParts() ok = false, want true")
	}

	want := HookPromptItem("msg-1", []HookPromptFragment{
		{Text: "Retry with care & joy.", HookRunID: "hook-run-1"},
	})
	if !reflect.DeepEqual(item, want) {
		t.Fatalf("item = %#v, want %#v", item, want)
	}
}

func TestVisibleUserInputItemSkipsContextualOnlyInput(t *testing.T) {
	t.Parallel()

	_, ok := VisibleUserInputItem("", "", []ContentPart{
		{Kind: ContentPartText, Text: "# AGENTS.md instructions for test_directory\n\n<INSTRUCTIONS>\ntest_text\n</INSTRUCTIONS>"},
		{Kind: ContentPartText, Text: "<environment_context>test_text</environment_context>"},
		{Kind: ContentPartText, Text: "<skill>\n<name>demo</name>\n<path>skills/demo/SKILL.md</path>\nbody\n</skill>"},
		{Kind: ContentPartText, Text: "<user_shell_command>echo 42</user_shell_command>"},
		{Kind: ContentPartText, Text: "<recommended_plugins>\n- Google Drive (google-drive@openai-curated-remote)\n</recommended_plugins>"},
		{Kind: ContentPartText, Text: "<goal_context>\nContinue working toward the active thread goal.\n</goal_context>"},
		{Kind: ContentPartText, Text: "<codex_internal_context source=\"extension\">\nInternal steering.\n</codex_internal_context>"},
		{Kind: ContentPartText, Text: "<SUBAGENT_NOTIFICATION>{}</subagent_notification>"},
	})
	if ok {
		t.Fatalf("VisibleUserInputItem() ok = true, want false for contextual-only input")
	}
}

func TestVisibleUserInputItemRejectsOnlyRegisteredContextualWrappers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
	}{
		{name: "arbitrary context tag", text: "<project_context>\nbody\n</project_context>"},
		{name: "agents header without wrapper", text: "# AGENTS.md instructions for /tmp\n\nbody"},
		{name: "invalid internal context source", text: "<codex_internal_context source=\"Extension\">\nbody\n</codex_internal_context>"},
		{name: "mismatched additional context close tag", text: "<external_browser_info>body</external_terminal_info>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			item, ok := VisibleUserInputItem("", "", []ContentPart{
				{Kind: ContentPartText, Text: tt.text},
			})
			if !ok {
				t.Fatalf("VisibleUserInputItem() ok = false, want ordinary user input")
			}
			want := UserInputItemWithParts(tt.text, []ContentPart{{Kind: ContentPartText, Text: tt.text}})
			if !reflect.DeepEqual(item, want) {
				t.Fatalf("item = %#v, want %#v", item, want)
			}
		})
	}
}

func TestVisibleUserInputItemKeepsOrdinaryUserText(t *testing.T) {
	t.Parallel()

	item, ok := VisibleUserInputItem("", "hello", []ContentPart{
		{Kind: ContentPartText, Text: "hello"},
	})
	if !ok {
		t.Fatalf("VisibleUserInputItem() ok = false, want true")
	}
	want := UserInputItemWithParts("hello", []ContentPart{{Kind: ContentPartText, Text: "hello"}})
	if !reflect.DeepEqual(item, want) {
		t.Fatalf("item = %#v, want %#v", item, want)
	}
}

// Adapted from Codex event_mapping `parses_user_message_with_text_and_two_images`.
// Dexco does not expose raw Responses API ContentItem values in its library API,
// so the equivalent invariant is preserved on normalized ContentPart input.
func TestVisibleUserInputItemParsesTextAndTwoImages(t *testing.T) {
	t.Parallel()

	img1 := "https://example.com/one.png"
	img2 := "https://example.com/two.jpg"
	parts := []ContentPart{
		{Kind: ContentPartText, Text: "Hello world"},
		{Kind: ContentPartImage, ImageURL: img1, Detail: "high"},
		{Kind: ContentPartImage, ImageURL: img2, Detail: "high"},
	}

	item, ok := VisibleUserInputItem("", "", parts)
	if !ok {
		t.Fatalf("VisibleUserInputItem() ok = false, want true")
	}
	want := UserInputItemWithParts("Hello world", parts)
	if !reflect.DeepEqual(item, want) {
		t.Fatalf("item = %#v, want %#v", item, want)
	}
}

// Adapted from Codex event_mapping `skips_local_image_label_text`. The
// `<image name=...>` and closing tag are prompt scaffolding around the adjacent
// image, not visible user text.
func TestVisibleUserInputItemSkipsLocalImageLabelText(t *testing.T) {
	t.Parallel()

	image := ContentPart{Kind: ContentPartImage, ImageURL: "data:image/png;base64,abc", Detail: "high"}
	userText := ContentPart{Kind: ContentPartText, Text: "Please review this image."}

	item, ok := VisibleUserInputItem("", "", []ContentPart{
		{Kind: ContentPartText, Text: `<image name=[Image #1] path="/tmp/local.png">`},
		image,
		{Kind: ContentPartText, Text: "</image>"},
		userText,
	})
	if !ok {
		t.Fatalf("VisibleUserInputItem() ok = false, want true")
	}
	want := UserInputItemWithParts(userText.Text, []ContentPart{image, userText})
	if !reflect.DeepEqual(item, want) {
		t.Fatalf("item = %#v, want %#v", item, want)
	}
}

// Adapted from Codex event_mapping `skips_unnamed_image_label_text`. Remote or
// unnamed image labels use the shorter `<image>` wrapper and follow the same
// adjacency rule as local-image labels.
func TestVisibleUserInputItemSkipsUnnamedImageLabelText(t *testing.T) {
	t.Parallel()

	image := ContentPart{Kind: ContentPartImage, ImageURL: "data:image/png;base64,abc", Detail: "high"}
	userText := ContentPart{Kind: ContentPartText, Text: "Please review this image."}

	item, ok := VisibleUserInputItem("", "", []ContentPart{
		{Kind: ContentPartText, Text: "<image>"},
		image,
		{Kind: ContentPartText, Text: "</image>"},
		userText,
	})
	if !ok {
		t.Fatalf("VisibleUserInputItem() ok = false, want true")
	}
	want := UserInputItemWithParts(userText.Text, []ContentPart{image, userText})
	if !reflect.DeepEqual(item, want) {
		t.Fatalf("item = %#v, want %#v", item, want)
	}
}

// Codex hides any user message that carries contextual fragments such as
// environment context or AGENTS.md instructions. Hook prompts are parsed before
// this check; ordinary contextual messages do not become user-visible history.
func TestVisibleUserInputItemSkipsMixedContextualInput(t *testing.T) {
	t.Parallel()

	_, ok := VisibleUserInputItem("", "", []ContentPart{
		{Kind: ContentPartText, Text: "<environment_context>ctx</environment_context>"},
		{Kind: ContentPartText, Text: "hello"},
	})
	if ok {
		t.Fatalf("VisibleUserInputItem() ok = true, want false for mixed contextual input")
	}
}

// Adapted from Codex event_mapping
// `parses_assistant_message_input_text_for_backward_compatibility`. Dexco's
// normalized ContentPart text accepts both the older input_text shape and the
// newer output_text shape at the assistant-message boundary.
func TestAssistantMessageItemFromPartsAcceptsTextParts(t *testing.T) {
	t.Parallel()

	content := "author: /root\nrecipient: /root/worker\nother_recipients: []\nContent: continue"
	item := AssistantMessageItemFromParts([]ContentPart{{Kind: ContentPartText, Text: content}})
	want := AssistantMessageItem(content)
	if !reflect.DeepEqual(item, want) {
		t.Fatalf("item = %#v, want %#v", item, want)
	}
}
