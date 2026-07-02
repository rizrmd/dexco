package imageprep

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/rizrmd/dexco/internal/model"
)

const (
	DetailHigh     = "high"
	DetailAuto     = "auto"
	DetailOriginal = "original"
	DetailLow      = "low"

	maxHighImageSide     = 2048
	maxHighImagePixels   = 1600 * 1600
	maxOriginalImageSide = 6000
	maxOriginalPixels    = 3200 * 3200
)

// Budget captures the image downsizing policy Codex applies before content is
// sent back into the model. Dexco keeps the values here, not in session or tool
// code, so Codex changes to multimodal budgets can be ported in one place.
type Budget struct {
	MaxSide   int
	MaxPixels int
}

var (
	HighBudget     = Budget{MaxSide: maxHighImageSide, MaxPixels: maxHighImagePixels}
	OriginalBudget = Budget{MaxSide: maxOriginalImageSide, MaxPixels: maxOriginalPixels}
	NoBudget       = Budget{}
)

// UserInputItem converts Dexco's public UserInput shape into a model-visible
// history item. This mirrors Codex's user-input image preparation: remote URLs
// are replaced with bounded placeholders, local files are read eagerly, and
// image bytes are resized before they become part of the prompt.
func UserInputItem(input model.UserInput) model.Item {
	parts := make([]model.ContentPart, 0, len(input.Parts))
	imageLabel := 0
	for _, part := range input.Parts {
		parts = append(parts, prepareUserPart(part, &imageLabel)...)
	}
	return model.UserInputItemWithParts(input.Content, parts)
}

// PrepareLocalImage reads a local image and returns a PNG data-url content part
// using the supplied budget. view_image uses this with NoBudget for
// detail=original, while user-turn local image input uses OriginalBudget to
// match Codex's request-preparation cap.
func PrepareLocalImage(path string, detail string, budget Budget) (model.ContentPart, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return model.ContentPart{}, fmt.Errorf("read image: %w", err)
	}
	if mimeType := detectedMIME(path, data); !supportedImageMIME(mimeType) {
		return model.ContentPart{}, UnsupportedImageError{MIME: mimeType}
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return model.ContentPart{}, fmt.Errorf("decode image: %w", err)
	}
	imageURL, err := encodePNGDataURL(resizeToBudget(decoded, budget))
	if err != nil {
		return model.ContentPart{}, err
	}
	return model.ContentPart{
		Kind:     model.ContentPartImage,
		ImageURL: imageURL,
		Detail:   detail,
		Path:     path,
	}, nil
}

type UnsupportedImageError struct {
	MIME string
}

func (e UnsupportedImageError) Error() string {
	return fmt.Sprintf("unsupported image `%s`", e.MIME)
}

func prepareUserPart(part model.ContentPart, imageLabel *int) []model.ContentPart {
	switch part.Kind {
	case model.ContentPartText:
		if part.Text == "" {
			return nil
		}
		return []model.ContentPart{{Kind: model.ContentPartText, Text: part.Text}}
	case model.ContentPartImage:
		*imageLabel = *imageLabel + 1
		return prepareUserImagePart(part, *imageLabel)
	default:
		return []model.ContentPart{textPlaceholder(
			fmt.Sprintf("Dexco ignored unsupported user content part kind `%s`.", part.Kind),
		)}
	}
}

func prepareUserImagePart(part model.ContentPart, labelNumber int) []model.ContentPart {
	if part.Path == "" && part.ImageURL != "" && !strings.HasPrefix(strings.ToLower(part.ImageURL), "data:") {
		// Codex classifies URL-only network images as unsupported remote images
		// before applying detail policy. That keeps the model-visible placeholder
		// actionable even when the model also supplied a detail value such as
		// `low`: the embedder must provide a data URL or local path first.
		return []model.ContentPart{textPlaceholder("Dexco cannot attach remote image URLs; provide a data URL or local path.")}
	}

	detail, budget, placeholder := userDetailPolicy(part.Detail)
	if placeholder != "" {
		return []model.ContentPart{textPlaceholder(placeholder)}
	}

	if part.Path != "" {
		prepared, err := PrepareLocalImage(part.Path, detail, budget)
		if err != nil {
			return []model.ContentPart{textPlaceholder(localImagePlaceholder(part.Path, err))}
		}
		return []model.ContentPart{
			{Kind: model.ContentPartText, Text: localImageOpenTag(labelNumber, part.Path)},
			prepared,
			{Kind: model.ContentPartText, Text: imageCloseTag()},
		}
	}
	if part.ImageURL == "" {
		return []model.ContentPart{textPlaceholder("Dexco cannot attach image input without an image URL or local path.")}
	}
	prepared, err := prepareDataURLImage(part.ImageURL, detail, budget)
	if err != nil {
		return []model.ContentPart{textPlaceholder("Dexco could not process this image attachment.")}
	}
	return []model.ContentPart{prepared}
}

func userDetailPolicy(detail string) (string, Budget, string) {
	switch strings.TrimSpace(strings.ToLower(detail)) {
	case "", DetailHigh, DetailAuto:
		return DetailHigh, HighBudget, ""
	case DetailOriginal:
		return DetailOriginal, OriginalBudget, ""
	case DetailLow:
		return "", Budget{}, "Dexco cannot attach low-detail images; use `high` or `original`."
	default:
		return "", Budget{}, fmt.Sprintf("Dexco cannot attach image with unsupported detail `%s`; use `high` or `original`.", detail)
	}
}

func prepareDataURLImage(imageURL string, detail string, budget Budget) (model.ContentPart, error) {
	prefix, payload, ok := strings.Cut(imageURL, ",")
	if !ok || !strings.Contains(strings.ToLower(prefix), ";base64") {
		return model.ContentPart{}, fmt.Errorf("invalid base64 data URL")
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return model.ContentPart{}, fmt.Errorf("decode data URL: %w", err)
	}
	if mimeType := dataURLMIME(prefix); !supportedImageMIME(mimeType) {
		return model.ContentPart{}, UnsupportedImageError{MIME: mimeType}
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return model.ContentPart{}, fmt.Errorf("decode image: %w", err)
	}
	resized := resizeToBudget(decoded, budget)
	if sameBounds(decoded, resized) {
		return model.ContentPart{
			Kind:     model.ContentPartImage,
			ImageURL: imageURL,
			Detail:   detail,
		}, nil
	}
	resizedURL, err := encodePNGDataURL(resized)
	if err != nil {
		return model.ContentPart{}, err
	}
	return model.ContentPart{
		Kind:     model.ContentPartImage,
		ImageURL: resizedURL,
		Detail:   detail,
	}, nil
}

func localImagePlaceholder(path string, err error) string {
	var unsupported UnsupportedImageError
	if errors.As(err, &unsupported) {
		return fmt.Sprintf("Dexco cannot attach image at `%s`: %s.", path, unsupported.Error())
	}
	return fmt.Sprintf("Dexco cannot attach image at `%s`: could not read or decode image.", path)
}

func localImageOpenTag(labelNumber int, path string) string {
	// Codex labels local images with text spans around the image content so the
	// model has a visible filename/path anchor. The sequence is shared with
	// data-url images, matching Codex's mixed remote/local image behavior.
	return fmt.Sprintf("<image name=[Image #%d] path=\"%s\">", labelNumber, path)
}

func imageCloseTag() string {
	return "</image>"
}

func textPlaceholder(text string) model.ContentPart {
	return model.ContentPart{Kind: model.ContentPartText, Text: text}
}

func detectedMIME(path string, data []byte) string {
	if ext := strings.ToLower(filepath.Ext(path)); ext != "" {
		if ext == ".svg" {
			return "image/svg+xml"
		}
		if ext == ".json" {
			return "application/json"
		}
		if mimeType := mime.TypeByExtension(ext); mimeType != "" {
			return strings.Split(mimeType, ";")[0]
		}
	}
	return strings.Split(http.DetectContentType(data), ";")[0]
}

func dataURLMIME(prefix string) string {
	mediaType := strings.TrimPrefix(prefix, "data:")
	mediaType = strings.Split(mediaType, ";")[0]
	if mediaType == "" {
		return "text/plain"
	}
	return strings.ToLower(mediaType)
}

func supportedImageMIME(mimeType string) bool {
	switch strings.ToLower(mimeType) {
	case "image/png", "image/jpeg", "image/gif":
		return true
	default:
		return false
	}
}

func resizeToBudget(src image.Image, budget Budget) image.Image {
	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 || (budget.MaxSide <= 0 && budget.MaxPixels <= 0) {
		return src
	}

	scale := 1.0
	if budget.MaxSide > 0 && width > budget.MaxSide {
		scale = math.Min(scale, float64(budget.MaxSide)/float64(width))
	}
	if budget.MaxSide > 0 && height > budget.MaxSide {
		scale = math.Min(scale, float64(budget.MaxSide)/float64(height))
	}
	if budget.MaxPixels > 0 {
		if pixels := width * height; pixels > budget.MaxPixels {
			scale = math.Min(scale, math.Sqrt(float64(budget.MaxPixels)/float64(pixels)))
		}
	}
	if scale >= 1 {
		return src
	}

	nextWidth := max(1, int(math.Round(float64(width)*scale)))
	nextHeight := max(1, int(math.Round(float64(height)*scale)))
	dst := image.NewRGBA(image.Rect(0, 0, nextWidth, nextHeight))
	for y := range nextHeight {
		sourceY := bounds.Min.Y + min(height-1, int(float64(y)/scale))
		for x := range nextWidth {
			sourceX := bounds.Min.X + min(width-1, int(float64(x)/scale))
			dst.Set(x, y, src.At(sourceX, sourceY))
		}
	}
	return dst
}

func sameBounds(a image.Image, b image.Image) bool {
	return a.Bounds().Dx() == b.Bounds().Dx() && a.Bounds().Dy() == b.Bounds().Dy()
}

func encodePNGDataURL(src image.Image) (string, error) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, src); err != nil {
		return "", fmt.Errorf("encode image: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes()), nil
}
