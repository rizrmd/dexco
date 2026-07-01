package model

import (
	"reflect"
	"testing"
)

// Adapted from Codex's assistant text stream parser tests. Citations can be
// split across output_item.added and output_text.delta events; visible text must
// stream without the hidden `<oai-mem-citation>` payload.
func TestAssistantTextParserStripsCitationsAcrossChunks(t *testing.T) {
	t.Parallel()

	parser := NewAssistantTextParser()
	seeded := parser.Push("hello <oai-mem-citation>doc")
	parsed := parser.Push("1</oai-mem-citation> world")
	tail := parser.Finish()

	if seeded.VisibleText != "hello " {
		t.Fatalf("seeded visible text = %q, want %q", seeded.VisibleText, "hello ")
	}
	if seeded.MemoryCitation != nil {
		t.Fatalf("seeded citation = %#v, want nil", seeded.MemoryCitation)
	}
	if parsed.VisibleText != " world" {
		t.Fatalf("parsed visible text = %q, want %q", parsed.VisibleText, " world")
	}
	if parsed.MemoryCitation != nil {
		t.Fatalf("parsed citation = %#v, want nil for unstructured citation body", parsed.MemoryCitation)
	}
	if tail.VisibleText != "" || tail.MemoryCitation != nil {
		t.Fatalf("tail = %#v, want empty", tail)
	}
}

// Adapted from Codex memory citation parsing. Dexco strips the hidden marker
// from assistant text and keeps structured citation entries plus rollout IDs on
// the assistant item for clients that render provenance.
func TestStripMemoryCitationsParsesStructuredCitation(t *testing.T) {
	t.Parallel()

	visible, citation := StripMemoryCitations(
		"hello<oai-mem-citation><citation_entries>\nMEMORY.md:1-2|note=[x]\n</citation_entries>\n<rollout_ids>\n019cc2ea-1dff-7902-8d40-c8f6e5d83cc4\n</rollout_ids></oai-mem-citation> world",
	)

	if visible != "hello world" {
		t.Fatalf("visible text = %q, want %q", visible, "hello world")
	}
	want := &MemoryCitation{
		Entries: []MemoryCitationEntry{{
			Path:      "MEMORY.md",
			LineStart: 1,
			LineEnd:   2,
			Note:      "x",
		}},
		RolloutIDs: []string{"019cc2ea-1dff-7902-8d40-c8f6e5d83cc4"},
	}
	if !reflect.DeepEqual(citation, want) {
		t.Fatalf("citation = %#v, want %#v", citation, want)
	}
}

func TestParseMemoryCitationSupportsThreadIDsAndDedupes(t *testing.T) {
	t.Parallel()

	citation := ParseMemoryCitation([]string{
		"<thread_ids>\nthread-1\nthread-1\nthread-2\n</thread_ids>",
	})

	want := &MemoryCitation{RolloutIDs: []string{"thread-1", "thread-2"}}
	if !reflect.DeepEqual(citation, want) {
		t.Fatalf("citation = %#v, want %#v", citation, want)
	}
}

func TestAssistantTextParserPreservesPartialOpenTagAtFinish(t *testing.T) {
	t.Parallel()

	parser := NewAssistantTextParser()
	chunk := parser.Push("hello <oai-mem-")
	tail := parser.Finish()

	if chunk.VisibleText != "hello " {
		t.Fatalf("chunk visible text = %q, want %q", chunk.VisibleText, "hello ")
	}
	if tail.VisibleText != "<oai-mem-" {
		t.Fatalf("tail visible text = %q, want %q", tail.VisibleText, "<oai-mem-")
	}
}

func TestAssistantTextParserAutoClosesUnterminatedCitation(t *testing.T) {
	t.Parallel()

	visible, citation := StripMemoryCitations(
		"x<oai-mem-citation><rollout_ids>\nrollout-1\n</rollout_ids>",
	)

	if visible != "x" {
		t.Fatalf("visible text = %q, want %q", visible, "x")
	}
	want := &MemoryCitation{RolloutIDs: []string{"rollout-1"}}
	if !reflect.DeepEqual(citation, want) {
		t.Fatalf("citation = %#v, want %#v", citation, want)
	}
}
