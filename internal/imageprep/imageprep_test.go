package imageprep

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rizrmd/dexco/internal/model"
)

// Adapted from Codex core's user-turn local-image tests. Dexco prepares image
// input at the library prompt boundary instead of in a Responses serializer,
// but the model-visible invariant is the same: high-detail images are resized
// before they enter history.
func TestUserInputLocalImageHighDetailResizes(t *testing.T) {
	t.Parallel()

	path := writePNG(t, image.Pt(2304, 864))
	item := UserInputItem(model.UserInput{
		Content: "look",
		Parts: []model.ContentPart{
			{Kind: model.ContentPartImage, Path: path},
		},
	})

	imagePart := imagePartFromUserItem(t, item)
	if imagePart.Detail != DetailHigh {
		t.Fatalf("Detail = %q, want high", imagePart.Detail)
	}
	if got := decodeBounds(t, imagePart.ImageURL); got != image.Pt(2048, 768) {
		t.Fatalf("bounds = %v, want 2048x768", got)
	}
}

// Codex keeps a larger, still bounded, budget for original-detail user images.
// This guards against accidentally applying the smaller high-detail budget to
// explicit original attachments.
func TestUserInputLocalImageOriginalDetailUsesOriginalBudget(t *testing.T) {
	t.Parallel()

	path := writePNG(t, image.Pt(6401, 100))
	item := UserInputItem(model.UserInput{
		Parts: []model.ContentPart{
			{Kind: model.ContentPartImage, Path: path, Detail: DetailOriginal},
		},
	})

	imagePart := imagePartFromUserItem(t, item)
	if imagePart.Detail != DetailOriginal {
		t.Fatalf("Detail = %q, want original", imagePart.Detail)
	}
	if got := decodeBounds(t, imagePart.ImageURL); got != image.Pt(6000, 94) {
		t.Fatalf("bounds = %v, want 6000x94", got)
	}
}

// Adapted from Codex core's image preparation tests. Small data-url images
// should remain byte-for-byte stable so Dexco does not churn already-valid
// multimodal input, while remote URLs become bounded placeholder text because
// Dexco does not fetch arbitrary network images inside the library loop.
func TestUserInputDataURLSmallImagePreservesBytesAndRemoteURLBecomesPlaceholder(t *testing.T) {
	t.Parallel()

	dataURL := pngDataURL(t, image.Pt(64, 32))
	item := UserInputItem(model.UserInput{
		Parts: []model.ContentPart{
			{Kind: model.ContentPartImage, ImageURL: dataURL, Detail: DetailHigh},
			{Kind: model.ContentPartImage, ImageURL: "https://example.com/image.png", Detail: DetailLow},
		},
	})

	if len(item.Parts) != 2 {
		t.Fatalf("Parts = %#v, want preserved image and remote placeholder", item.Parts)
	}
	if item.Parts[0].Kind != model.ContentPartImage || item.Parts[0].ImageURL != dataURL {
		t.Fatalf("first part = %#v, want original data URL image", item.Parts[0])
	}
	if item.Parts[1].Kind != model.ContentPartText ||
		!strings.Contains(item.Parts[1].Text, "remote image URLs") {
		t.Fatalf("second part = %#v, want remote URL placeholder", item.Parts[1])
	}
}

// Codex maps `auto` and omitted detail to the high-detail preparation budget,
// applies the larger original budget only when explicitly requested, and
// rejects `low` detail before image bytes enter the prompt.
func TestUserInputImageDetailPoliciesMatchCodexBudgets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		detail     string
		size       image.Point
		wantDetail string
		wantBounds image.Point
		wantText   string
	}{
		{
			name:       "auto uses high budget",
			detail:     DetailAuto,
			size:       image.Pt(2048, 2048),
			wantDetail: DetailHigh,
			wantBounds: image.Pt(1600, 1600),
		},
		{
			name:       "omitted uses high budget",
			size:       image.Pt(2048, 2048),
			wantDetail: DetailHigh,
			wantBounds: image.Pt(1600, 1600),
		},
		{
			name:       "original uses original budget",
			detail:     DetailOriginal,
			size:       image.Pt(3201, 3201),
			wantDetail: DetailOriginal,
			wantBounds: image.Pt(3200, 3200),
		},
		{
			name:     "low becomes placeholder",
			detail:   DetailLow,
			size:     image.Pt(64, 32),
			wantText: "low-detail images",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			item := UserInputItem(model.UserInput{
				Parts: []model.ContentPart{{
					Kind:     model.ContentPartImage,
					ImageURL: pngDataURL(t, tt.size),
					Detail:   tt.detail,
				}},
			})

			if tt.wantText != "" {
				if len(item.Parts) != 1 || item.Parts[0].Kind != model.ContentPartText {
					t.Fatalf("Parts = %#v, want one placeholder text part", item.Parts)
				}
				if !strings.Contains(item.Parts[0].Text, tt.wantText) {
					t.Fatalf("placeholder = %q, want containing %q", item.Parts[0].Text, tt.wantText)
				}
				return
			}

			if len(item.Parts) != 1 || item.Parts[0].Kind != model.ContentPartImage {
				t.Fatalf("Parts = %#v, want one image part", item.Parts)
			}
			part := item.Parts[0]
			if part.Detail != tt.wantDetail {
				t.Fatalf("Detail = %q, want %q", part.Detail, tt.wantDetail)
			}
			if got := decodeBounds(t, part.ImageURL); got != tt.wantBounds {
				t.Fatalf("bounds = %v, want %v", got, tt.wantBounds)
			}
		})
	}
}

func TestUserInputMixedDataURLAndLocalImagesShareLabelSequence(t *testing.T) {
	t.Parallel()

	dataURLPath := writePNG(t, image.Pt(1, 1))
	data, err := os.ReadFile(dataURLPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	localPath := writePNG(t, image.Pt(1, 1))
	item := UserInputItem(model.UserInput{
		Parts: []model.ContentPart{
			{
				Kind:     model.ContentPartImage,
				ImageURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(data),
			},
			{Kind: model.ContentPartImage, Path: localPath},
		},
	})

	if len(item.Parts) != 4 {
		t.Fatalf("Parts = %#v, want data image, local open tag, local image, local close tag", item.Parts)
	}
	if item.Parts[0].Kind != model.ContentPartImage {
		t.Fatalf("first part = %#v, want image", item.Parts[0])
	}
	wantTag := "<image name=[Image #2] path=\"" + localPath + "\">"
	if item.Parts[1].Text != wantTag {
		t.Fatalf("local image tag = %q, want %q", item.Parts[1].Text, wantTag)
	}
}

// Codex turns local image preparation failures into model-visible placeholder
// text instead of failing the whole turn. Dexco follows that library contract so
// a missing or invalid attachment does not poison session state.
func TestUserInputLocalImageFailuresBecomePlaceholders(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "example.json")
	if err := os.WriteFile(path, []byte(`{"hello":"world"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	item := UserInputItem(model.UserInput{
		Parts: []model.ContentPart{
			{Kind: model.ContentPartImage, Path: path},
		},
	})

	if len(item.Parts) != 1 || item.Parts[0].Kind != model.ContentPartText {
		t.Fatalf("Parts = %#v, want one placeholder text part", item.Parts)
	}
	text := item.Parts[0].Text
	if !strings.Contains(text, "unsupported image `application/json`") {
		t.Fatalf("placeholder = %q, want unsupported JSON MIME", text)
	}
	if !strings.Contains(text, path) {
		t.Fatalf("placeholder = %q, want path %q", text, path)
	}
}

func writePNG(t *testing.T, size image.Point) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "example.png")
	img := image.NewRGBA(image.Rect(0, 0, size.X, size.Y))
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

func pngDataURL(t *testing.T, size image.Point) string {
	t.Helper()
	path := writePNG(t, size)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
}

func imagePartFromUserItem(t *testing.T, item model.Item) model.ContentPart {
	t.Helper()
	if item.Kind != model.ItemUserInput {
		t.Fatalf("Kind = %q, want user_input", item.Kind)
	}
	if len(item.Parts) != 3 {
		t.Fatalf("Parts = %#v, want open text, image, close text", item.Parts)
	}
	if item.Parts[0].Kind != model.ContentPartText || !strings.HasPrefix(item.Parts[0].Text, "<image name=[Image #1]") {
		t.Fatalf("open image tag = %#v", item.Parts[0])
	}
	if item.Parts[2].Kind != model.ContentPartText || item.Parts[2].Text != "</image>" {
		t.Fatalf("close image tag = %#v", item.Parts[2])
	}
	part := item.Parts[1]
	if part.Kind != model.ContentPartImage {
		t.Fatalf("image part = %#v, want image", part)
	}
	return part
}

func decodeBounds(t *testing.T, imageURL string) image.Point {
	t.Helper()
	encoded := strings.TrimPrefix(imageURL, "data:image/png;base64,")
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
