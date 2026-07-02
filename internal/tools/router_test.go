package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/rizrmd/dexco/internal/model"
)

// Adapted from Codex core's unknown custom-tool handling. Unknown tools should
// become failed tool outputs that the model can observe, not hard runner errors.
func TestRouterDispatchReturnsFailedResultForUnknownTool(t *testing.T) {
	t.Parallel()

	router, err := NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}

	item, err := router.Dispatch(context.Background(), model.ToolCall{
		CallID: "call-unknown",
		Name:   "unknown_tool",
	})
	if err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}

	want := model.ToolResultItem(model.ToolResult{
		CallID:  "call-unknown",
		Name:    "unknown_tool",
		Output:  `unknown tool "unknown_tool"`,
		Success: false,
	})
	if !reflect.DeepEqual(item, want) {
		t.Fatalf("Dispatch() = %#v, want %#v", item, want)
	}
}

// Adapted from Codex router/registry namespace tests. Dexco models namespaced
// tools as ordinary exact string names; the important parity invariant is that
// there is no fallback between a plain local tool and a namespaced tool.
func TestRouterDispatchMatchesNamespacedNamesExactly(t *testing.T) {
	t.Parallel()

	router, err := NewRouter(staticTool{name: "echo"}, staticTool{name: "mcp__server__echo"})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}

	plain, err := router.Dispatch(context.Background(), model.ToolCall{
		CallID: "plain",
		Name:   "echo",
	})
	if err != nil {
		t.Fatalf("Dispatch(plain) error = %v", err)
	}
	namespaced, err := router.Dispatch(context.Background(), model.ToolCall{
		CallID: "namespaced",
		Name:   "mcp__server__echo",
	})
	if err != nil {
		t.Fatalf("Dispatch(namespaced) error = %v", err)
	}
	missing, err := router.Dispatch(context.Background(), model.ToolCall{
		CallID: "missing",
		Name:   "mcp__other__echo",
	})
	if err != nil {
		t.Fatalf("Dispatch(missing) error = %v", err)
	}

	if !containsToolOutput(plain, "echo") {
		t.Fatalf("plain Dispatch() = %#v, want plain handler output", plain)
	}
	if !containsToolOutput(namespaced, "mcp__server__echo") {
		t.Fatalf("namespaced Dispatch() = %#v, want namespaced handler output", namespaced)
	}
	wantMissing := model.ToolResultItem(model.ToolResult{
		CallID:  "missing",
		Name:    "mcp__other__echo",
		Output:  `unknown tool "mcp__other__echo"`,
		Success: false,
	})
	if !reflect.DeepEqual(missing, wantMissing) {
		t.Fatalf("missing Dispatch() = %#v, want %#v", missing, wantMissing)
	}
}

// Adapted from Codex router parallel-support tests. Parallel execution is an
// exact-handler opt-in; registering a parallel local tool must not make an
// unregistered namespaced alias parallel-safe.
func TestRouterSupportsParallelRequiresExactRegisteredOptIn(t *testing.T) {
	t.Parallel()

	router, err := NewRouter(parallelStaticTool{staticTool: staticTool{name: "echo"}})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}

	if !router.SupportsParallel(model.ToolCall{Name: "echo"}) {
		t.Fatalf("SupportsParallel(echo) = false, want true")
	}
	if router.SupportsParallel(model.ToolCall{Name: "mcp__server__echo"}) {
		t.Fatalf("SupportsParallel(mcp__server__echo) = true, want false")
	}
}

func TestRouterDeferredToolsAreSearchableButNotAdvertised(t *testing.T) {
	t.Parallel()

	router, err := NewRouter(staticTool{name: "direct_tool", description: "visible direct tool"})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	if err := router.RegisterDeferred(staticTool{
		name:        "hidden_weather",
		description: "gets forecast data",
		parameters:  map[string]any{"city": map[string]any{"type": "string"}},
	}, "weather forecast rain"); err != nil {
		t.Fatalf("RegisterDeferred() error = %v", err)
	}

	specs := router.Specs()
	gotNames := make([]string, 0, len(specs))
	for _, spec := range specs {
		gotNames = append(gotNames, spec.Name)
	}
	wantNames := []string{"direct_tool", "tool_search"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("Specs names = %#v, want %#v", gotNames, wantNames)
	}

	searchResult, err := router.Dispatch(context.Background(), model.ToolCall{
		CallID:    "search-call",
		Name:      "tool_search",
		Arguments: json.RawMessage(`{"query":"forecast"}`),
	})
	if err != nil {
		t.Fatalf("Dispatch(tool_search) error = %v", err)
	}
	if !containsToolOutput(searchResult, `"name":"hidden_weather"`) ||
		!containsToolOutput(searchResult, `"defer_loading":true`) {
		t.Fatalf("tool_search result = %#v, want hidden descriptor", searchResult)
	}

	hiddenResult, err := router.Dispatch(context.Background(), model.ToolCall{
		CallID: "hidden-call",
		Name:   "hidden_weather",
	})
	if err != nil {
		t.Fatalf("Dispatch(hidden_weather) error = %v", err)
	}
	if !containsToolOutput(hiddenResult, "hidden_weather") {
		t.Fatalf("hidden Dispatch() = %#v, want hidden handler output", hiddenResult)
	}
}

// Adapted from Codex search_tool tests for matching deferred/dynamic tools by
// distinct name, description, namespace, and schema terms. Dexco has a simpler
// provider-neutral deferred registry, but the same discoverability invariant
// applies: embedders should not have to duplicate searchable spec metadata in
// RegisterDeferred's extra search text.
func TestRouterToolSearchMatchesNameDescriptionAndSchemaTerms(t *testing.T) {
	t.Parallel()

	router, err := NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	if err := router.RegisterDeferred(staticTool{
		name:        "mcp__orbit_ops__quasar_ping_beacon",
		description: "Uploads a document to the saffron metronome archive.",
		parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"chrono_spec": map[string]any{
					"type":        "string",
					"description": "Start time for the orbit operation.",
				},
			},
		},
	}, ""); err != nil {
		t.Fatalf("RegisterDeferred() error = %v", err)
	}

	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "raw tool name", query: "quasar_ping_beacon"},
		{name: "spaced tool name", query: "quasar ping beacon"},
		{name: "namespace", query: "orbit ops"},
		{name: "description", query: "uploaded document"},
		{name: "schema raw", query: "chrono_spec"},
		{name: "schema spaced", query: "chrono spec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item, err := router.Dispatch(context.Background(), model.ToolCall{
				CallID:    tc.name,
				Name:      "tool_search",
				Arguments: json.RawMessage(`{"query":` + quoteJSONString(t, tc.query) + `}`),
			})
			if err != nil {
				t.Fatalf("Dispatch(tool_search) error = %v", err)
			}
			if !containsToolOutput(item, `"name":"mcp__orbit_ops__quasar_ping_beacon"`) {
				t.Fatalf("tool_search(%q) = %#v, want deferred descriptor", tc.query, item)
			}
		})
	}
}

func TestRouterToolSearchValidatesQueryAndLimit(t *testing.T) {
	t.Parallel()

	router, err := NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	if err := router.RegisterDeferred(staticTool{name: "hidden_tool"}, "hidden"); err != nil {
		t.Fatalf("RegisterDeferred() error = %v", err)
	}

	for _, tc := range []struct {
		name      string
		arguments string
		want      string
	}{
		{name: "empty query", arguments: `{"query":"   "}`, want: "non-empty query"},
		{name: "zero limit", arguments: `{"query":"hidden","limit":0}`, want: "limit must be greater than zero"},
		{name: "negative limit", arguments: `{"query":"hidden","limit":-1}`, want: "limit must be greater than zero"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item, err := router.Dispatch(context.Background(), model.ToolCall{
				CallID:    tc.name,
				Name:      "tool_search",
				Arguments: json.RawMessage(tc.arguments),
			})
			if err != nil {
				t.Fatalf("Dispatch(tool_search) error = %v", err)
			}
			if item.ToolResult == nil || item.ToolResult.Success {
				t.Fatalf("Dispatch(tool_search) = %#v, want failed tool result", item)
			}
			if !strings.Contains(item.ToolResult.Output, tc.want) {
				t.Fatalf("Output = %q, want containing %q", item.ToolResult.Output, tc.want)
			}
		})
	}
}

type staticTool struct {
	name        string
	description string
	parameters  map[string]any
}

func (t staticTool) Name() string {
	return t.name
}

func (t staticTool) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        t.name,
		Description: t.description,
		Parameters:  t.parameters,
	}
}

func (t staticTool) Call(context.Context, model.ToolCall) (model.ToolResult, error) {
	return model.ToolResult{Output: t.name, Success: true}, nil
}

type parallelStaticTool struct {
	staticTool
}

func (parallelStaticTool) SupportsParallel() bool {
	return true
}

func containsToolOutput(item model.Item, output string) bool {
	return item.ToolResult != nil && strings.Contains(item.ToolResult.Output, output) && item.ToolResult.Success
}

func quoteJSONString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal string: %v", err)
	}
	return string(encoded)
}
