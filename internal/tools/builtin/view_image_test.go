package builtin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rizrmd/dexco/internal/model"
)

// Adapted from Codex view_image local-image tests. Dexco does not own Codex's
// request serializer, but the portable invariant is that high-detail local
// images are bounded before becoming model-visible image content.
func TestViewImageHandlerAttachesResizedHighDetailImages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		size       image.Point
		wantBounds image.Point
	}{
		{
			name:       "wide image max side",
			size:       image.Pt(2304, 864),
			wantBounds: image.Pt(2048, 768),
		},
		{
			name:       "vertical image max side",
			size:       image.Pt(1024, 4096),
			wantBounds: image.Pt(512, 2048),
		},
		{
			name:       "square image patch budget",
			size:       image.Pt(2048, 2048),
			wantBounds: image.Pt(1600, 1600),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := writeTestPNG(t, tt.size)
			result, err := ViewImageHandler{}.Call(context.Background(), model.ToolCall{
				CallID:    "view-image-call",
				Name:      "view_image",
				Arguments: json.RawMessage(`{"path":` + quoteJSON(t, path) + `}`),
			})
			if err != nil {
				t.Fatalf("Call() error = %v", err)
			}

			if !result.Success {
				t.Fatalf("Success = false, want true")
			}
			part := singleImagePart(t, result)
			if part.Detail != "high" {
				t.Fatalf("Detail = %q, want high", part.Detail)
			}
			gotBounds := decodeImagePartBounds(t, part)
			if gotBounds != tt.wantBounds {
				t.Fatalf("image bounds = %v, want %v", gotBounds, tt.wantBounds)
			}
		})
	}
}

// Adapted from Codex view_image `original` detail coverage. Original detail
// should preserve dimensions rather than applying the high-detail resize budget.
func TestViewImageHandlerOriginalDetailPreservesDimensions(t *testing.T) {
	t.Parallel()

	path := writeTestPNG(t, image.Pt(2304, 864))
	result, err := ViewImageHandler{}.Call(context.Background(), model.ToolCall{
		CallID:    "view-image-call",
		Name:      "view_image",
		Arguments: json.RawMessage(`{"path":` + quoteJSON(t, path) + `,"detail":"original"}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	part := singleImagePart(t, result)
	if part.Detail != "original" {
		t.Fatalf("Detail = %q, want original", part.Detail)
	}
	gotBounds := decodeImagePartBounds(t, part)
	if gotBounds != image.Pt(2304, 864) {
		t.Fatalf("image bounds = %v, want 2304x864", gotBounds)
	}
}

// Adapted from Codex view_image unsupported-detail and null-detail coverage.
// Unsupported detail values must become clear failed text results, while JSON
// null behaves like omitted/default high detail.
func TestViewImageHandlerDetailValidation(t *testing.T) {
	t.Parallel()

	path := writeTestPNG(t, image.Pt(2304, 864))
	unsupported, err := ViewImageHandler{}.Call(context.Background(), model.ToolCall{
		CallID:    "view-image-call",
		Name:      "view_image",
		Arguments: json.RawMessage(`{"path":` + quoteJSON(t, path) + `,"detail":"low"}`),
	})
	if err != nil {
		t.Fatalf("Call(low) error = %v", err)
	}
	if unsupported.Success {
		t.Fatalf("Success = true, want false")
	}
	wantOutput := "view_image.detail only supports `high` or `original`; omit `detail` for default high resized behavior, got `low`"
	if unsupported.Output != wantOutput {
		t.Fatalf("Output = %q, want %q", unsupported.Output, wantOutput)
	}
	if len(unsupported.Parts) != 0 {
		t.Fatalf("Parts = %#v, want none", unsupported.Parts)
	}

	nullDetail, err := ViewImageHandler{}.Call(context.Background(), model.ToolCall{
		CallID:    "view-image-call",
		Name:      "view_image",
		Arguments: json.RawMessage(`{"path":` + quoteJSON(t, path) + `,"detail":null}`),
	})
	if err != nil {
		t.Fatalf("Call(null) error = %v", err)
	}
	part := singleImagePart(t, nullDetail)
	if part.Detail != "high" {
		t.Fatalf("null detail produced Detail = %q, want high", part.Detail)
	}
	if gotBounds := decodeImagePartBounds(t, part); gotBounds != image.Pt(2048, 768) {
		t.Fatalf("null detail image bounds = %v, want 2048x768", gotBounds)
	}
}

func TestViewImageHandlerGuardrailIsReadOnly(t *testing.T) {
	t.Parallel()

	guardrail, err := ViewImageHandler{}.Guardrail(context.Background(), model.ToolCall{
		CallID: "view-image-call",
		Name:   "view_image",
	})
	if err != nil {
		t.Fatalf("Guardrail() error = %v", err)
	}

	want := model.ToolGuardrail{
		Risk:                model.ToolRiskReadOnly,
		ApprovalRequirement: model.ApprovalRequirementNone,
		Reason:              "read-only image attachment",
	}
	if !reflect.DeepEqual(guardrail, want) {
		t.Fatalf("Guardrail() = %#v, want %#v", guardrail, want)
	}
}

func writeTestPNG(t *testing.T, size image.Point) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "example.png")
	img := image.NewRGBA(image.Rect(0, 0, size.X, size.Y))
	for y := range size.Y {
		for x := range size.X {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 80, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	return path
}

func quoteJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return string(encoded)
}

func singleImagePart(t *testing.T, result model.ToolResult) model.ContentPart {
	t.Helper()
	if result.Output != "" {
		t.Fatalf("Output = %q, want empty image content output", result.Output)
	}
	if len(result.Parts) != 1 || result.Parts[0].Kind != model.ContentPartImage {
		t.Fatalf("Parts = %#v, want one image part", result.Parts)
	}
	if !strings.HasPrefix(result.Parts[0].ImageURL, "data:image/png;base64,") {
		t.Fatalf("ImageURL = %q, want PNG data URL", result.Parts[0].ImageURL)
	}
	return result.Parts[0]
}

func decodeImagePartBounds(t *testing.T, part model.ContentPart) image.Point {
	t.Helper()
	encoded := strings.TrimPrefix(part.ImageURL, "data:image/png;base64,")
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	bounds := decoded.Bounds()
	return image.Pt(bounds.Dx(), bounds.Dy())
}
