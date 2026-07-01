package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/openai/codex/dexco/internal/model"
)

type Handler interface {
	Name() string
	Spec() model.ToolSpec
	Call(ctx context.Context, call model.ToolCall) (model.ToolResult, error)
}

// GuardrailHandler is optional so ordinary library tools stay small. Handlers
// that can perform side effects should implement it to provide the same early
// classification point Codex uses before routing a shell/MCP/app action through
// approval and sandbox policy.
type GuardrailHandler interface {
	Guardrail(ctx context.Context, call model.ToolCall) (model.ToolGuardrail, error)
}

// ParallelHandler is an explicit opt-in. This follows Codex's safety invariant:
// a tool is serial unless its implementation declares that concurrent calls do
// not share unsafe mutable state or observable ordering dependencies.
type ParallelHandler interface {
	SupportsParallel() bool
}

// PendingInputInterruptHandler lets a tool opt into Codex pending-input wakeups.
// Long-running wait/sleep-style tools should implement this and return a normal
// model-visible result when their call context is canceled by new input.
type PendingInputInterruptHandler interface {
	InterruptsOnPendingInput() bool
}

type Router struct {
	handlers map[string]registeredHandler
}

// DispatchOutcome carries non-model-visible metadata from routing to the runner.
// Codex keeps this distinction in its ToolCallOutcome enum; Dexco keeps it
// internal so handler errors can still be surfaced to lifecycle hooks without
// leaking implementation details into model-visible ToolResult output.
type DispatchOutcome struct {
	HandlerExecuted bool
	HandlerError    bool
}

// registeredHandler mirrors the Codex deferred-tool registry shape in a
// provider-neutral way: every tool is registered for dispatch, but deferred
// tools are hidden from prompt Specs until the model finds them through the
// synthetic `tool_search` handler.
type registeredHandler struct {
	handler    Handler
	deferred   bool
	searchText string
}

type toolSearchArgs struct {
	Query string `json:"query"`
	Limit *int   `json:"limit,omitempty"`
}

type toolSearchResponse struct {
	Tools []toolSearchDescriptor `json:"tools"`
}

type toolSearchDescriptor struct {
	Name         string         `json:"name"`
	Description  string         `json:"description,omitempty"`
	Parameters   map[string]any `json:"parameters,omitempty"`
	DeferLoading bool           `json:"defer_loading"`
}

func NewRouter(handlers ...Handler) (*Router, error) {
	router := &Router{
		handlers: make(map[string]registeredHandler, len(handlers)),
	}

	for _, handler := range handlers {
		if err := router.Register(handler); err != nil {
			return nil, err
		}
	}

	return router, nil
}

func (r *Router) Register(handler Handler) error {
	return r.register(handler, false, "")
}

// RegisterDeferred adds a tool that can be called by exact name after discovery
// but is omitted from initial prompt tool specs. Dexco keeps this seam close to
// Codex's deferred tool loading behavior so future Codex search/indexing
// improvements can be ported without changing handler implementations.
func (r *Router) RegisterDeferred(handler Handler, searchText string) error {
	return r.register(handler, true, searchText)
}

func (r *Router) register(handler Handler, deferred bool, searchText string) error {
	if handler == nil {
		return fmt.Errorf("register handler: nil handler")
	}

	name := handler.Name()
	if name == "" {
		return fmt.Errorf("register handler: empty name")
	}
	if _, exists := r.handlers[name]; exists {
		return fmt.Errorf("register handler %q: already registered", name)
	}

	if name == "tool_search" {
		return fmt.Errorf("register handler %q: reserved for deferred tool discovery", name)
	}

	r.handlers[name] = registeredHandler{
		handler:    handler,
		deferred:   deferred,
		searchText: searchText,
	}
	return nil
}

func (r *Router) Specs() []model.ToolSpec {
	specs := make([]model.ToolSpec, 0, len(r.handlers))
	hasDeferred := false
	for _, registered := range r.handlers {
		if registered.deferred {
			hasDeferred = true
			continue
		}
		specs = append(specs, registered.handler.Spec())
	}
	if hasDeferred {
		specs = append(specs, toolSearchSpec())
	}

	sort.Slice(specs, func(i, j int) bool {
		return specs[i].Name < specs[j].Name
	})

	return specs
}

func (r *Router) Dispatch(ctx context.Context, call model.ToolCall) (model.Item, error) {
	item, _, err := r.DispatchWithOutcome(ctx, call)
	return item, err
}

func (r *Router) DispatchWithOutcome(ctx context.Context, call model.ToolCall) (model.Item, DispatchOutcome, error) {
	if call.Name == "tool_search" {
		return r.dispatchToolSearch(call), DispatchOutcome{HandlerExecuted: true}, nil
	}

	registered, ok := r.handlers[call.Name]
	if !ok {
		return model.ToolResultItem(model.ToolResult{
			CallID:  call.CallID,
			Name:    call.Name,
			Output:  fmt.Sprintf("unknown tool %q", call.Name),
			Success: false,
		}), DispatchOutcome{}, nil
	}
	handler := registered.handler

	result, err := handler.Call(ctx, call)
	if err != nil {
		return model.ToolResultItem(model.ToolResult{
			CallID:  call.CallID,
			Name:    call.Name,
			Output:  err.Error(),
			Success: false,
		}), DispatchOutcome{HandlerExecuted: true, HandlerError: true}, nil
	}

	if result.CallID == "" {
		result.CallID = call.CallID
	}
	if result.Name == "" {
		result.Name = call.Name
	}

	return model.ToolResultItem(result), DispatchOutcome{HandlerExecuted: true}, nil
}

func (r *Router) Guardrail(ctx context.Context, call model.ToolCall) (model.ToolGuardrail, error) {
	if call.Name == "tool_search" {
		return model.ToolGuardrail{
			Risk:                model.ToolRiskReadOnly,
			ApprovalRequirement: model.ApprovalRequirementNone,
			Reason:              "search deferred tool metadata",
		}, nil
	}
	registered, ok := r.handlers[call.Name]
	if !ok {
		// Unknown tools are dispatched as failed tool outputs later, so the
		// guardrail layer does not need to reject them here.
		return model.ToolGuardrail{
			Risk:                model.ToolRiskUnknown,
			ApprovalRequirement: model.ApprovalRequirementNone,
			Reason:              fmt.Sprintf("unknown tool %q", call.Name),
		}, nil
	}
	handler := registered.handler

	guardrailHandler, ok := handler.(GuardrailHandler)
	if !ok {
		return model.ToolGuardrail{
			Risk:                model.ToolRiskUnknown,
			ApprovalRequirement: model.ApprovalRequirementNone,
		}, nil
	}

	guardrail, err := guardrailHandler.Guardrail(ctx, call)
	if err != nil {
		return model.ToolGuardrail{}, err
	}
	if guardrail.Risk == "" {
		guardrail.Risk = model.ToolRiskUnknown
	}
	if guardrail.ApprovalRequirement == "" {
		guardrail.ApprovalRequirement = model.ApprovalRequirementNone
	}
	return guardrail, nil
}

func (r *Router) SupportsParallel(call model.ToolCall) bool {
	registered, ok := r.handlers[call.Name]
	if !ok {
		return false
	}
	handler := registered.handler
	parallelHandler, ok := handler.(ParallelHandler)
	return ok && parallelHandler.SupportsParallel()
}

func (r *Router) InterruptsOnPendingInput(call model.ToolCall) bool {
	registered, ok := r.handlers[call.Name]
	if !ok {
		return false
	}
	handler := registered.handler
	interruptHandler, ok := handler.(PendingInputInterruptHandler)
	return ok && interruptHandler.InterruptsOnPendingInput()
}

func toolSearchSpec() model.ToolSpec {
	return model.ToolSpec{
		Name:        "tool_search",
		Description: "Searches deferred tool metadata and returns loadable tool descriptors.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query for deferred tools.",
				},
				"limit": map[string]any{
					"type":        "number",
					"description": "Maximum number of tool descriptors to return. Defaults to 8.",
				},
			},
			"required": []string{"query"},
		},
	}
}

// dispatchToolSearch is Dexco's adaptation of Codex's richer search-tool event
// path. Codex emits provider-specific `tool_search_call`/output items; Dexco
// intentionally returns an ordinary ToolResult containing bounded loadable
// descriptors so the library API stays model-provider agnostic.
func (r *Router) dispatchToolSearch(call model.ToolCall) model.Item {
	var args toolSearchArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return model.ToolResultItem(model.ToolResult{
			CallID:  call.CallID,
			Name:    call.Name,
			Output:  fmt.Sprintf("parse tool_search arguments: %v", err),
			Success: false,
		})
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return model.ToolResultItem(model.ToolResult{
			CallID:  call.CallID,
			Name:    call.Name,
			Output:  "tool_search requires non-empty query",
			Success: false,
		})
	}
	limit := 8
	if args.Limit != nil {
		limit = *args.Limit
	}
	if limit <= 0 {
		return model.ToolResultItem(model.ToolResult{
			CallID:  call.CallID,
			Name:    call.Name,
			Output:  "tool_search limit must be greater than zero",
			Success: false,
		})
	}
	matches := r.searchDeferredTools(query)
	if len(matches) > limit {
		matches = matches[:limit]
	}
	response := toolSearchResponse{Tools: make([]toolSearchDescriptor, 0, len(matches))}
	for _, match := range matches {
		spec := match.handler.Spec()
		response.Tools = append(response.Tools, toolSearchDescriptor{
			Name:         spec.Name,
			Description:  spec.Description,
			Parameters:   spec.Parameters,
			DeferLoading: true,
		})
	}
	output, err := json.Marshal(response)
	if err != nil {
		return model.ToolResultItem(model.ToolResult{
			CallID:  call.CallID,
			Name:    call.Name,
			Output:  fmt.Sprintf("encode tool_search output: %v", err),
			Success: false,
		})
	}
	return model.ToolResultItem(model.ToolResult{
		CallID:  call.CallID,
		Name:    call.Name,
		Output:  string(output),
		Success: true,
	})
}

func (r *Router) searchDeferredTools(query string) []registeredHandler {
	terms := strings.Fields(strings.ToLower(query))
	type scored struct {
		score int
		name  string
		item  registeredHandler
	}
	scoredItems := make([]scored, 0)
	for name, registered := range r.handlers {
		if !registered.deferred {
			continue
		}
		text := strings.ToLower(deferredSearchText(registered))
		score := 0
		for _, term := range terms {
			if strings.Contains(text, term) {
				score++
			}
		}
		if score == 0 {
			continue
		}
		scoredItems = append(scoredItems, scored{
			score: score,
			name:  name,
			item:  registered,
		})
	}
	sort.Slice(scoredItems, func(i, j int) bool {
		if scoredItems[i].score != scoredItems[j].score {
			return scoredItems[i].score > scoredItems[j].score
		}
		return scoredItems[i].name < scoredItems[j].name
	})
	results := make([]registeredHandler, 0, len(scoredItems))
	for _, item := range scoredItems {
		results = append(results, item.item)
	}
	return results
}

func deferredSearchText(registered registeredHandler) string {
	spec := registered.handler.Spec()
	var builder strings.Builder
	appendDeferredSearchText(&builder, spec.Name)
	appendDeferredSearchText(&builder, spec.Description)
	appendDeferredSearchText(&builder, registered.searchText)
	if len(spec.Parameters) > 0 {
		encoded, err := json.Marshal(spec.Parameters)
		if err == nil {
			appendDeferredSearchText(&builder, string(encoded))
		}
	}
	return builder.String()
}

func appendDeferredSearchText(builder *strings.Builder, text string) {
	if text == "" {
		return
	}
	if builder.Len() > 0 {
		builder.WriteByte('\n')
	}
	builder.WriteString(text)
	normalized := normalizedDeferredSearchText(text)
	if normalized != text {
		builder.WriteByte('\n')
		builder.WriteString(normalized)
	}
}

func normalizedDeferredSearchText(text string) string {
	replacer := strings.NewReplacer(
		"_", " ",
		"-", " ",
		".", " ",
		"/", " ",
	)
	return strings.Join(strings.Fields(replacer.Replace(text)), " ")
}
