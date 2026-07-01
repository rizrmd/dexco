package model

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// ToolTelemetryPreviewMaxBytes and ToolTelemetryPreviewMaxLines mirror
	// Codex's telemetry preview caps from core/src/tools/context.rs. Keep these
	// small relative to model context budgets so audit/client events can include
	// previews without accidentally becoming another unbounded output path.
	ToolTelemetryPreviewMaxBytes = 2 * 1024
	ToolTelemetryPreviewMaxLines = 64
)

const ToolTelemetryPreviewTruncationNotice = "[... telemetry preview truncated ...]"

// ToolResultWithWallTime adapts Codex's MCP/tool output envelope to Dexco's
// provider-neutral ToolResult. Text-only output receives the wall-time header
// followed by the original text. Rich content receives the same header as the
// first text part so adapters can preserve images/encrypted parts instead of
// flattening them into plain text.
func ToolResultWithWallTime(result ToolResult, wallTime time.Duration) ToolResult {
	header := fmt.Sprintf("Wall time: %.4f seconds\nOutput:", wallTime.Seconds())
	if len(result.Parts) > 0 {
		parts := make([]ContentPart, 0, len(result.Parts)+1)
		parts = append(parts, ContentPart{Kind: ContentPartText, Text: header})
		parts = append(parts, result.Parts...)
		result.Parts = parts
	}
	if result.Output == "" {
		result.Output = header
		return result
	}
	result.Output = header + "\n" + result.Output
	return result
}

// ToolTelemetryPreview is Dexco's public equivalent of Codex's telemetry_preview
// helper. It truncates first by UTF-8 byte budget and then by line budget,
// appending a fixed notice only when truncation happened.
func ToolTelemetryPreview(content string) string {
	truncatedSlice := takeBytesAtUTF8Boundary(content, ToolTelemetryPreviewMaxBytes)
	truncatedByBytes := len(truncatedSlice) < len(content)

	preview, truncatedByLines := takeLines(truncatedSlice, ToolTelemetryPreviewMaxLines)
	if !truncatedByBytes && !truncatedByLines {
		return content
	}
	if preview != "" && !strings.HasSuffix(preview, "\n") {
		preview += "\n"
	}
	return preview + ToolTelemetryPreviewTruncationNotice
}

func takeBytesAtUTF8Boundary(content string, maxBytes int) string {
	if maxBytes < 0 || len(content) <= maxBytes {
		return content
	}
	end := maxBytes
	for end > 0 && end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	return content[:end]
}

func takeLines(content string, maxLines int) (string, bool) {
	if maxLines < 0 {
		return content, false
	}
	if maxLines == 0 {
		return "", content != ""
	}
	lineEnd := 0
	for range maxLines {
		next := strings.IndexByte(content[lineEnd:], '\n')
		if next == -1 {
			return content, false
		}
		lineEnd += next + 1
		if lineEnd == len(content) {
			return content, false
		}
	}
	return content[:lineEnd], true
}
