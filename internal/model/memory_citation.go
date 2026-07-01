package model

import (
	"strconv"
	"strings"
)

const (
	memoryCitationOpenTag  = "<oai-mem-citation>"
	memoryCitationCloseTag = "</oai-mem-citation>"
)

// MemoryCitation is Dexco's provider-neutral copy of Codex's assistant memory
// citation payload. Codex strips `<oai-mem-citation>` tags from assistant text
// before it becomes visible, but keeps parsed citation metadata on the final
// assistant item so clients can render provenance without polluting chat text.
type MemoryCitation struct {
	Entries    []MemoryCitationEntry
	RolloutIDs []string
}

type MemoryCitationEntry struct {
	Path      string
	LineStart uint32
	LineEnd   uint32
	Note      string
}

type AssistantTextChunk struct {
	VisibleText    string
	MemoryCitation *MemoryCitation
}

// AssistantTextParser mirrors Codex's assistant stream parser for the portable
// citation behavior: hide memory-citation markup from visible text, support tags
// split across output_item.added and text deltas, and auto-close an unterminated
// citation at finish. Dexco omits Codex's plan-mode parsing here because plan
// items are not a library-level concept in the current Go surface.
type AssistantTextParser struct {
	pending      string
	citationBody strings.Builder
	inCitation   bool
}

func NewAssistantTextParser() *AssistantTextParser {
	return &AssistantTextParser{}
}

func (p *AssistantTextParser) Push(text string) AssistantTextChunk {
	if p == nil || text == "" {
		return AssistantTextChunk{}
	}
	p.pending += text
	return p.drain( /*finishing*/ false)
}

func (p *AssistantTextParser) Finish() AssistantTextChunk {
	if p == nil {
		return AssistantTextChunk{}
	}
	return p.drain( /*finishing*/ true)
}

func (p *AssistantTextParser) drain(finishing bool) AssistantTextChunk {
	var visible strings.Builder
	citations := make([]string, 0)

	for p.pending != "" {
		if p.inCitation {
			if idx := strings.Index(p.pending, memoryCitationCloseTag); idx >= 0 {
				p.citationBody.WriteString(p.pending[:idx])
				citations = append(citations, p.citationBody.String())
				p.citationBody.Reset()
				p.pending = p.pending[idx+len(memoryCitationCloseTag):]
				p.inCitation = false
				continue
			}
			if finishing {
				p.citationBody.WriteString(p.pending)
				p.pending = ""
				citations = append(citations, p.citationBody.String())
				p.citationBody.Reset()
				p.inCitation = false
				break
			}
			keep := longestSuffixPrefix(p.pending, memoryCitationCloseTag)
			p.citationBody.WriteString(p.pending[:len(p.pending)-keep])
			p.pending = p.pending[len(p.pending)-keep:]
			break
		}

		if idx := strings.Index(p.pending, memoryCitationOpenTag); idx >= 0 {
			visible.WriteString(p.pending[:idx])
			p.pending = p.pending[idx+len(memoryCitationOpenTag):]
			p.inCitation = true
			continue
		}
		if finishing {
			visible.WriteString(p.pending)
			p.pending = ""
			break
		}
		keep := longestSuffixPrefix(p.pending, memoryCitationOpenTag)
		visible.WriteString(p.pending[:len(p.pending)-keep])
		p.pending = p.pending[len(p.pending)-keep:]
		break
	}
	if finishing && p.inCitation {
		citations = append(citations, p.citationBody.String())
		p.citationBody.Reset()
		p.inCitation = false
	}

	return AssistantTextChunk{
		VisibleText:    visible.String(),
		MemoryCitation: ParseMemoryCitation(citations),
	}
}

func StripMemoryCitations(text string) (string, *MemoryCitation) {
	parser := NewAssistantTextParser()
	chunk := parser.Push(text)
	tail := parser.Finish()
	return chunk.VisibleText + tail.VisibleText, MergeMemoryCitations(chunk.MemoryCitation, tail.MemoryCitation)
}

func NormalizeAssistantMessageItem(item Item) Item {
	if item.Kind != ItemAssistantMessage {
		return item
	}
	visible, citation := StripMemoryCitations(item.Content)
	item.Content = visible
	item.MemoryCitation = MergeMemoryCitations(item.MemoryCitation, citation)
	return item
}

func ParseMemoryCitation(citations []string) *MemoryCitation {
	var parsed MemoryCitation
	seenRolloutIDs := make(map[string]struct{})

	for _, citation := range citations {
		if entriesBlock, ok := extractBlock(citation, "<citation_entries>", "</citation_entries>"); ok {
			for _, line := range strings.Split(entriesBlock, "\n") {
				if entry, ok := parseMemoryCitationEntry(line); ok {
					parsed.Entries = append(parsed.Entries, entry)
				}
			}
		}
		if idsBlock, ok := extractIDsBlock(citation); ok {
			for _, line := range strings.Split(idsBlock, "\n") {
				id := strings.TrimSpace(line)
				if id == "" {
					continue
				}
				if _, ok := seenRolloutIDs[id]; ok {
					continue
				}
				seenRolloutIDs[id] = struct{}{}
				parsed.RolloutIDs = append(parsed.RolloutIDs, id)
			}
		}
	}

	if len(parsed.Entries) == 0 && len(parsed.RolloutIDs) == 0 {
		return nil
	}
	return &parsed
}

func MergeMemoryCitations(left *MemoryCitation, right *MemoryCitation) *MemoryCitation {
	if left == nil {
		return CloneMemoryCitation(right)
	}
	if right == nil {
		return CloneMemoryCitation(left)
	}

	merged := MemoryCitation{
		Entries:    append([]MemoryCitationEntry(nil), left.Entries...),
		RolloutIDs: append([]string(nil), left.RolloutIDs...),
	}
	merged.Entries = append(merged.Entries, right.Entries...)
	seen := make(map[string]struct{}, len(merged.RolloutIDs))
	for _, id := range merged.RolloutIDs {
		seen[id] = struct{}{}
	}
	for _, id := range right.RolloutIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		merged.RolloutIDs = append(merged.RolloutIDs, id)
	}
	return &merged
}

func CloneMemoryCitation(citation *MemoryCitation) *MemoryCitation {
	if citation == nil {
		return nil
	}
	return &MemoryCitation{
		Entries:    append([]MemoryCitationEntry(nil), citation.Entries...),
		RolloutIDs: append([]string(nil), citation.RolloutIDs...),
	}
}

func parseMemoryCitationEntry(line string) (MemoryCitationEntry, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return MemoryCitationEntry{}, false
	}
	location, noteWithSuffix, ok := strings.Cut(line, "|note=[")
	if !ok {
		return MemoryCitationEntry{}, false
	}
	note, ok := strings.CutSuffix(noteWithSuffix, "]")
	if !ok {
		return MemoryCitationEntry{}, false
	}
	location = strings.TrimSpace(location)
	lineRangeSep := strings.LastIndex(location, ":")
	if lineRangeSep < 0 {
		return MemoryCitationEntry{}, false
	}
	path := location[:lineRangeSep]
	lineRange := location[lineRangeSep+1:]
	rangeStart, rangeEnd, ok := strings.Cut(lineRange, "-")
	if !ok {
		return MemoryCitationEntry{}, false
	}
	lineStart, err := strconv.ParseUint(strings.TrimSpace(rangeStart), 10, 32)
	if err != nil {
		return MemoryCitationEntry{}, false
	}
	lineEnd, err := strconv.ParseUint(strings.TrimSpace(rangeEnd), 10, 32)
	if err != nil {
		return MemoryCitationEntry{}, false
	}
	return MemoryCitationEntry{
		Path:      strings.TrimSpace(path),
		LineStart: uint32(lineStart),
		LineEnd:   uint32(lineEnd),
		Note:      strings.TrimSpace(note),
	}, true
}

func extractIDsBlock(text string) (string, bool) {
	if block, ok := extractBlock(text, "<rollout_ids>", "</rollout_ids>"); ok {
		return block, true
	}
	return extractBlock(text, "<thread_ids>", "</thread_ids>")
}

func extractBlock(text string, open string, close string) (string, bool) {
	_, rest, ok := strings.Cut(text, open)
	if !ok {
		return "", false
	}
	body, _, ok := strings.Cut(rest, close)
	return body, ok
}

func longestSuffixPrefix(value string, prefixOf string) int {
	max := len(value)
	if len(prefixOf)-1 < max {
		max = len(prefixOf) - 1
	}
	for size := max; size > 0; size-- {
		if strings.HasPrefix(prefixOf, value[len(value)-size:]) {
			return size
		}
	}
	return 0
}
