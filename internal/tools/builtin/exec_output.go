package builtin

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

type shellEncodingCandidate struct {
	name    string
	decoder *charmap.Charmap
}

var shellEncodingCandidates = []shellEncodingCandidate{
	{name: "windows-1251", decoder: charmap.Windows1251},
	{name: "cp866", decoder: charmap.CodePage866},
	{name: "windows-1252", decoder: charmap.Windows1252},
	{name: "iso-8859-1", decoder: charmap.ISO8859_1},
}

// decodeShellOutput adapts Codex protocol's bytes_to_string_smart behavior for
// Dexco's smaller exec tool. Codex uses chardetng + encoding_rs so Windows/VS
// Code shell bytes such as CP1251, CP866, and Windows-1252 punctuation do not
// become mojibake or replacement characters. Go does not ship the same detector
// in the standard library, so Dexco keeps a bounded heuristic over the same
// high-value code pages and falls back to lossy UTF-8 when the bytes do not look
// like plausible text.
func decodeShellOutput(bytes []byte) string {
	if len(bytes) == 0 {
		return ""
	}
	if utf8.Valid(bytes) {
		return string(bytes)
	}
	if looksLikeInvalidUTF16BOM(bytes) {
		return lossyUTF8(bytes)
	}
	if looksLikeWindows1252Punctuation(bytes) {
		return decodeCharmap(bytes, charmap.Windows1252)
	}

	bestText := ""
	bestScore := -1 << 30
	for _, candidate := range shellEncodingCandidates {
		text := decodeCharmap(bytes, candidate.decoder)
		score := scoreShellDecodedText(text)
		if score > bestScore {
			bestText = text
			bestScore = score
		}
	}
	if bestScore < 4 {
		return lossyUTF8(bytes)
	}
	return bestText
}

func decodeCharmap(bytes []byte, decoder *charmap.Charmap) string {
	text, err := decoder.NewDecoder().String(string(bytes))
	if err != nil {
		return lossyUTF8(bytes)
	}
	return text
}

func lossyUTF8(bytes []byte) string {
	return strings.ToValidUTF8(string(bytes), "\uFFFD")
}

func looksLikeInvalidUTF16BOM(bytes []byte) bool {
	if len(bytes) < 2 {
		return false
	}
	return bytes[0] == 0xFF && bytes[1] == 0xFE || bytes[0] == 0xFE && bytes[1] == 0xFF
}

// Windows-1252 maps 0x80-0x9F to smart punctuation while CP866 maps many of
// the same bytes to Cyrillic. Codex special-cases short shell snippets that mix
// those punctuation bytes with ASCII words so `“test”` does not render as CP866
// Cyrillic garbage. Dexco mirrors that targeted coercion before generic scoring.
func looksLikeWindows1252Punctuation(bytes []byte) bool {
	sawExtendedPunctuation := false
	sawASCIIWord := false
	for _, b := range bytes {
		if b >= 0xA0 {
			return false
		}
		if b >= 0x80 && b <= 0x9F {
			if !isWindows1252Punctuation(b) {
				return false
			}
			sawExtendedPunctuation = true
		}
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') {
			sawASCIIWord = true
		}
	}
	return sawExtendedPunctuation && sawASCIIWord
}

func isWindows1252Punctuation(b byte) bool {
	switch b {
	case 0x91, 0x92, 0x93, 0x94, 0x95, 0x96, 0x97, 0x99:
		return true
	default:
		return false
	}
}

func scoreShellDecodedText(text string) int {
	score := 0
	latinLetters := 0
	cyrillicLetters := 0

	for _, r := range text {
		switch {
		case r == utf8.RuneError:
			score -= 20
		case r == '\n' || r == '\r' || r == '\t':
			score += 1
		case unicode.IsControl(r):
			score -= 20
		case isASCIIPrintable(r):
			score += 2
			if unicode.IsLetter(r) {
				latinLetters++
			}
		case unicode.In(r, unicode.Cyrillic):
			score += 4
			cyrillicLetters++
			if isRareCyrillicMojibakeMarker(r) {
				score -= 8
			}
		case unicode.In(r, unicode.Latin) && unicode.IsLetter(r):
			score += 2
			latinLetters++
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			score -= 3
		case unicode.IsPrint(r):
			score += 1
		default:
			score -= 10
		}
	}

	if latinLetters > 0 && cyrillicLetters > 0 {
		score -= 8
	}
	return score
}

func isASCIIPrintable(r rune) bool {
	return r >= 0x20 && r <= 0x7E
}

func isRareCyrillicMojibakeMarker(r rune) bool {
	switch r {
	case 'Ё', 'Ї', 'Ґ':
		return true
	default:
		return false
	}
}
