package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestToolResultFromContentPartsPreservesMixedContent(t *testing.T) {
	got := ToolResultFromContentParts("call1", "inspect", []ContentPart{
		{Kind: ContentPartText, Text: "caption"},
		{Kind: ContentPartImage, ImageData: "BASE64", MIMEType: "image/png", Detail: "original"},
		{Kind: ContentPartText, Text: "follow-up"},
		{Kind: ContentPartEncrypted, EncryptedContent: "enc_opaque"},
	}, true)

	want := ToolResult{
		CallID: "call1",
		Name:   "inspect",
		Output: "caption\nfollow-up",
		Parts: []ContentPart{
			{Kind: ContentPartText, Text: "caption"},
			{Kind: ContentPartImage, ImageURL: "data:image/png;base64,BASE64", Detail: "original"},
			{Kind: ContentPartText, Text: "follow-up"},
			{Kind: ContentPartEncrypted, EncryptedContent: "enc_opaque"},
		},
		Success: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolResultFromContentParts() = %#v, want %#v", got, want)
	}
}

func TestToolResultFromContentPartsRemoteImageURLBecomesTextError(t *testing.T) {
	got := ToolResultFromContentParts("call1", "dynamic_tool", []ContentPart{
		{Kind: ContentPartText, Text: "before"},
		{Kind: ContentPartImage, ImageURL: "HTTPS://example.com/tool.png"},
	}, true)

	want := ToolResult{
		CallID: "call1",
		Name:   "dynamic_tool",
		Output: RemoteImageURLToolResultError,
		Parts: []ContentPart{{
			Kind: ContentPartText,
			Text: RemoteImageURLToolResultError,
		}},
		Success: false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolResultFromContentParts() = %#v, want %#v", got, want)
	}
}

func TestNormalizeToolResultPartsPreservesDataURLAndDefaultsImageDetail(t *testing.T) {
	got := NormalizeToolResultParts([]ContentPart{
		{Kind: ContentPartImage, ImageURL: "data:image/png;base64,BASE64"},
		{Kind: ContentPartImage, ImageData: "RAW", Detail: "unsupported"},
	})

	want := []ContentPart{
		{Kind: ContentPartImage, ImageURL: "data:image/png;base64,BASE64", Detail: "high"},
		{Kind: ContentPartImage, ImageURL: "data:application/octet-stream;base64,RAW", Detail: "high"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeToolResultParts() = %#v, want %#v", got, want)
	}
}

func TestToolResultWithoutRemoteImageURLsRejectsOnlyRemoteImageOutputs(t *testing.T) {
	allowed := ToolResult{
		CallID: "allowed",
		Name:   "dynamic_tool",
		Parts: []ContentPart{
			{Kind: ContentPartImage, ImageURL: "data:image/png;base64,AAA"},
			{Kind: ContentPartImage, ImageData: "RAW"},
		},
		Success: true,
	}
	if got := ToolResultWithoutRemoteImageURLs(allowed); !reflect.DeepEqual(got, allowed) {
		t.Fatalf("ToolResultWithoutRemoteImageURLs(data) = %#v, want %#v", got, allowed)
	}

	got := ToolResultWithoutRemoteImageURLs(ToolResult{
		CallID: "remote",
		Name:   "dynamic_tool",
		Output: "legacy text",
		Parts: []ContentPart{
			{Kind: ContentPartImage, ImageURL: "https://example.com/tool.png"},
		},
		Success: true,
	})
	want := ToolResult{
		CallID: "remote",
		Name:   "dynamic_tool",
		Output: RemoteImageURLToolResultError,
		Parts: []ContentPart{{
			Kind: ContentPartText,
			Text: RemoteImageURLToolResultError,
		}},
		Success: false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolResultWithoutRemoteImageURLs(remote) = %#v, want %#v", got, want)
	}
}

func TestToolResultPartsTextIgnoresBlankTextImagesAndEncryptedContent(t *testing.T) {
	got := ToolResultPartsText([]ContentPart{
		{Kind: ContentPartText, Text: "   "},
		{Kind: ContentPartImage, ImageURL: "data:image/png;base64,AAA", Detail: "high"},
		{Kind: ContentPartEncrypted, EncryptedContent: "enc_opaque"},
	})
	if got != "" {
		t.Fatalf("ToolResultPartsText() = %q, want empty string", got)
	}
}

func TestToolResultWithoutImageInputRewritesImageParts(t *testing.T) {
	original := ToolResult{
		CallID: "call1",
		Name:   "mcp_tool",
		Parts: []ContentPart{
			{Kind: ContentPartImage, ImageData: "Zm9v", MIMEType: "image/png"},
			{Kind: ContentPartText, Text: "hello"},
		},
		Success: true,
	}

	got := ToolResultWithoutImageInput(original)
	want := ToolResult{
		CallID: "call1",
		Name:   "mcp_tool",
		Output: "<image content omitted because you do not support image input>\nhello",
		Parts: []ContentPart{
			{Kind: ContentPartText, Text: "<image content omitted because you do not support image input>"},
			{Kind: ContentPartText, Text: "hello"},
		},
		Success: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolResultWithoutImageInput() = %#v, want %#v", got, want)
	}
	if original.Parts[0].Kind != ContentPartImage || original.Parts[0].ImageData != "Zm9v" {
		t.Fatalf("original result was mutated: %#v", original)
	}
}

func TestItemsWithoutImageDetailStripsCopiesAndPreservesOriginal(t *testing.T) {
	original := []Item{
		UserInputItemWithParts("", []ContentPart{
			{Kind: ContentPartImage, ImageURL: "https://example.com/image.png", Detail: "original"},
		}),
		ToolResultItem(ToolResult{
			CallID: "call1",
			Name:   "inspect",
			Parts: []ContentPart{
				{Kind: ContentPartText, Text: "caption"},
				{Kind: ContentPartImage, ImageURL: "data:image/png;base64,AAA", Detail: "high"},
			},
			Success: true,
		}),
	}

	got := ItemsWithoutImageDetail(original)
	want := []Item{
		UserInputItemWithParts("", []ContentPart{
			{Kind: ContentPartImage, ImageURL: "https://example.com/image.png"},
		}),
		ToolResultItem(ToolResult{
			CallID: "call1",
			Name:   "inspect",
			Parts: []ContentPart{
				{Kind: ContentPartText, Text: "caption"},
				{Kind: ContentPartImage, ImageURL: "data:image/png;base64,AAA"},
			},
			Success: true,
		}),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ItemsWithoutImageDetail() = %#v, want %#v", got, want)
	}
	if original[0].Parts[0].Detail != "original" {
		t.Fatalf("original user image detail = %q, want original", original[0].Parts[0].Detail)
	}
	if original[1].ToolResult.Parts[1].Detail != "high" {
		t.Fatalf("original tool image detail = %q, want high", original[1].ToolResult.Parts[1].Detail)
	}
}

func TestToolResultWithWallTimePrefixesTextOutput(t *testing.T) {
	body := `[{"type":"text","text":"done"}]`
	got := ToolResultWithWallTime(ToolResult{
		CallID:  "call1",
		Name:    "mcp_tool",
		Output:  body,
		Success: true,
	}, 1250*time.Millisecond)

	prefix := "Wall time: 1.2500 seconds\nOutput:\n"
	if !strings.HasPrefix(got.Output, prefix) {
		t.Fatalf("Output = %q, want wall-time prefix %q", got.Output, prefix)
	}
	var parsed []map[string]string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(got.Output, prefix)), &parsed); err != nil {
		t.Fatalf("wall-time payload is not JSON: %v", err)
	}
	want := ToolResult{
		CallID:  "call1",
		Name:    "mcp_tool",
		Output:  prefix + body,
		Success: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolResultWithWallTime() = %#v, want %#v", got, want)
	}
}

func TestToolResultWithWallTimePreservesRichContentParts(t *testing.T) {
	got := ToolResultWithWallTime(ToolResult{
		CallID: "call1",
		Name:   "mcp_tool",
		Parts: []ContentPart{
			{Kind: ContentPartImage, ImageURL: "data:image/png;base64,AAA", Detail: "high"},
		},
		Success: true,
	}, 500*time.Millisecond)

	header := "Wall time: 0.5000 seconds\nOutput:"
	want := ToolResult{
		CallID: "call1",
		Name:   "mcp_tool",
		Output: header,
		Parts: []ContentPart{
			{Kind: ContentPartText, Text: header},
			{Kind: ContentPartImage, ImageURL: "data:image/png;base64,AAA", Detail: "high"},
		},
		Success: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolResultWithWallTime() = %#v, want %#v", got, want)
	}
}

func TestToolTelemetryPreviewMatchesCodexLimits(t *testing.T) {
	if got := ToolTelemetryPreview("short output"); got != "short output" {
		t.Fatalf("short preview = %q, want original", got)
	}

	longBytes := strings.Repeat("x", ToolTelemetryPreviewMaxBytes+8)
	bytePreview := ToolTelemetryPreview(longBytes)
	if !strings.Contains(bytePreview, ToolTelemetryPreviewTruncationNotice) {
		t.Fatalf("byte preview missing truncation notice: %q", bytePreview)
	}
	if len(bytePreview) > ToolTelemetryPreviewMaxBytes+len(ToolTelemetryPreviewTruncationNotice)+1 {
		t.Fatalf("byte preview length = %d, want bounded", len(bytePreview))
	}

	lines := make([]string, 0, ToolTelemetryPreviewMaxLines+5)
	for i := 0; i < ToolTelemetryPreviewMaxLines+5; i++ {
		lines = append(lines, "line")
	}
	linePreview := ToolTelemetryPreview(strings.Join(lines, "\n"))
	linePreviewLines := strings.Split(linePreview, "\n")
	if len(linePreviewLines) > ToolTelemetryPreviewMaxLines+1 {
		t.Fatalf("line preview has %d lines, want bounded", len(linePreviewLines))
	}
	if linePreviewLines[len(linePreviewLines)-1] != ToolTelemetryPreviewTruncationNotice {
		t.Fatalf("line preview last line = %q, want truncation notice", linePreviewLines[len(linePreviewLines)-1])
	}

	unicodePreview := ToolTelemetryPreview(strings.Repeat("é", ToolTelemetryPreviewMaxBytes/2+4))
	if !utf8.ValidString(unicodePreview) {
		t.Fatalf("unicode preview is not valid UTF-8: %q", unicodePreview)
	}
}

func TestTruncateToolResultOutputBoundsTextAndPreservesMetadata(t *testing.T) {
	plan := &PlanUpdate{
		Explanation: "keep metadata",
		Plan:        []PlanStep{{Step: "inspect", Status: PlanStepCompleted}},
	}
	got := TruncateToolResultOutput(ToolResult{
		CallID:        "call1",
		Name:          "inspect",
		Output:        "0123456789abcdef",
		FinalResponse: "final reply should not be truncated",
		Parts: []ContentPart{
			{Kind: ContentPartText, Text: "alpha beta gamma delta"},
			{Kind: ContentPartImage, ImageURL: "data:image/png;base64,BASE64", Detail: "high"},
			{Kind: ContentPartEncrypted, EncryptedContent: "enc_opaque"},
			{Kind: ContentPartText, Text: "tail text"},
		},
		Success:    true,
		PlanUpdate: plan,
	}, 8)

	wantOutput := "Warning: truncated tool output (original character count: 16)\n\n0123\n... 8 characters truncated ...\ncdef"
	want := ToolResult{
		CallID:        "call1",
		Name:          "inspect",
		Output:        wantOutput,
		FinalResponse: "final reply should not be truncated",
		Parts: []ContentPart{
			{
				Kind: ContentPartText,
				Text: "Warning: truncated tool output (original character count: 22)\n\nalph\n... 14 characters truncated ...\nelta",
			},
			{Kind: ContentPartImage, ImageURL: "data:image/png;base64,BASE64", Detail: "high"},
			{Kind: ContentPartEncrypted, EncryptedContent: "enc_opaque"},
			{Kind: ContentPartText, Text: "[omitted 1 text tool-result parts ...]"},
		},
		Success:    true,
		PlanUpdate: plan,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TruncateToolResultOutput() = %#v, want %#v", got, want)
	}
	if strings.Contains(got.Output, "456789ab") {
		t.Fatalf("truncated output retained middle text: %q", got.Output)
	}
}

func TestTruncateToolResultOutputDoesNotDoubleTruncate(t *testing.T) {
	alreadyTruncated := "Warning: truncated output (original token count: 500)\nTotal output lines: 100\n\nhead\n... 400 tokens truncated ...\ntail"
	got := TruncateToolResultOutput(ToolResult{
		CallID: "call1",
		Name:   "inspect",
		Output: alreadyTruncated,
		Parts: []ContentPart{
			{Kind: ContentPartText, Text: alreadyTruncated},
		},
		Success: true,
	}, 8)

	want := ToolResult{
		CallID: "call1",
		Name:   "inspect",
		Output: alreadyTruncated,
		Parts: []ContentPart{
			{Kind: ContentPartText, Text: alreadyTruncated},
		},
		Success: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TruncateToolResultOutput() = %#v, want %#v", got, want)
	}
	if count := strings.Count(got.Output, "Warning: truncated output"); count != 1 {
		t.Fatalf("truncated output warning count = %d, want 1 in %q", count, got.Output)
	}
}
