package dexco_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openai/codex/dexco"
)

type profileTool struct {
	name          string
	visibility    dexco.CapabilityRequirement
	required      dexco.CapabilityRequirement
	requiredFunc  func(dexco.ToolCall) (dexco.CapabilityRequirement, error)
	progress      dexco.ProgressHint
	progressErr   error
	delay         time.Duration
	parallel      bool
	contextCheck  func(context.Context) error
	callCount     int
	callCountLock sync.Mutex
}

func (t *profileTool) Name() string {
	return t.name
}

func (t *profileTool) Spec() dexco.ToolSpec {
	return dexco.ToolSpec{
		Name:        t.name,
		Description: "profile test tool",
		Parameters: map[string]any{
			"type": "object",
		},
	}
}

func (t *profileTool) Visibility(context.Context, dexco.Principal) (dexco.CapabilityRequirement, error) {
	return t.visibility, nil
}

func (t *profileTool) RequiredCapabilities(_ context.Context, call dexco.ToolCall) (dexco.CapabilityRequirement, error) {
	if t.requiredFunc != nil {
		return t.requiredFunc(call)
	}
	return t.required, nil
}

func (t *profileTool) Progress(context.Context, dexco.ToolCall) (dexco.ProgressHint, error) {
	return t.progress, t.progressErr
}

func (t *profileTool) Call(ctx context.Context, call dexco.ToolCall) (dexco.ToolResult, error) {
	if t.contextCheck != nil {
		if err := t.contextCheck(ctx); err != nil {
			return dexco.ToolResult{}, err
		}
	}
	if t.delay > 0 {
		timer := time.NewTimer(t.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return dexco.ToolResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	t.callCountLock.Lock()
	t.callCount++
	t.callCountLock.Unlock()
	return dexco.ToolResult{
		CallID:  call.CallID,
		Name:    call.Name,
		Output:  "ran " + t.name,
		Success: true,
	}, nil
}

func (t *profileTool) Guardrail(context.Context, dexco.ToolCall) (dexco.ToolGuardrail, error) {
	return dexco.ToolGuardrail{
		Risk:                dexco.ToolRiskReadOnly,
		ApprovalRequirement: dexco.ApprovalRequirementNone,
		Reason:              "profile test read-only",
	}, nil
}

func (t *profileTool) SupportsParallel() bool {
	return t.parallel
}

func (t *profileTool) Calls() int {
	t.callCountLock.Lock()
	defer t.callCountLock.Unlock()
	return t.callCount
}

type deferredProfileTool struct {
	*profileTool
	searchText string
}

func (t deferredProfileTool) DeferredSearchText() string {
	return t.searchText
}

type profileClient struct {
	t       *testing.T
	call    dexco.ToolCall
	prompts []dexco.Prompt
}

func (c *profileClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	switch len(c.prompts) {
	case 1:
		item := dexco.ToolCallItem(c.call)
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 2:
		item := dexco.AssistantMessageItem("done")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", len(c.prompts))
		return nil, nil
	}
}

type profileSearchClient struct {
	t       *testing.T
	query   string
	prompts []dexco.Prompt
}

func (c *profileSearchClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	switch len(c.prompts) {
	case 1:
		args, _ := json.Marshal(map[string]any{"query": c.query})
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "search-call",
			Name:      "tool_search",
			Arguments: args,
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 2:
		item := dexco.AssistantMessageItem("done")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", len(c.prompts))
		return nil, nil
	}
}

type profileParallelClient struct {
	t       *testing.T
	calls   []dexco.ToolCall
	prompts []dexco.Prompt
}

func (c *profileParallelClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	switch len(c.prompts) {
	case 1:
		events := make([]dexco.ResponseEvent, 0, len(c.calls)+1)
		for _, call := range c.calls {
			item := dexco.ToolCallItem(call)
			events = append(events, dexco.ResponseEvent{Type: dexco.EventOutputItemDone, Item: &item})
		}
		events = append(events, dexco.ResponseEvent{Type: dexco.EventCompleted, EndTurn: boolPtr(true)})
		return &sliceStream{events: events}, nil
	case 2:
		item := dexco.AssistantMessageItem("done")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", len(c.prompts))
		return nil, nil
	}
}

type singlePromptClient struct {
	prompt dexco.Prompt
}

func (c *singlePromptClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompt = prompt
	item := dexco.AssistantMessageItem("done")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

type slowStartClient struct {
	delay time.Duration
}

func (c *slowStartClient) Stream(ctx context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	if c.delay > 0 {
		timer := time.NewTimer(c.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	item := dexco.AssistantMessageItem("done")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

type lockedClientEventSink struct {
	dexco.NopSink
	mu     sync.Mutex
	events []dexco.ClientEvent
}

func (s *lockedClientEventSink) OnClientEvent(_ context.Context, event dexco.ClientEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

func (s *lockedClientEventSink) Events() []dexco.ClientEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]dexco.ClientEvent(nil), s.events...)
}

func baseCapabilityProfile(handlers ...dexco.ProfileHandler) dexco.CapabilityProfile {
	return dexco.CapabilityProfile{
		ID:   "sales",
		Mode: dexco.ProfileModeSingleRole,
		Principal: dexco.Principal{
			UserID:       "user-1",
			TenantID:     "tenant-1",
			Capabilities: []string{"sales.create", "reports.public.read"},
		},
		Config:   dexco.Config{Instructions: "Sales persona."},
		Handlers: handlers,
	}
}

func TestValidateCapabilityProfileRejectsUnsafeProfiles(t *testing.T) {
	t.Parallel()

	validTool := &profileTool{name: "create_sale"}
	duplicateA := &profileTool{name: "duplicate_tool"}
	duplicateB := &profileTool{name: "duplicate_tool"}

	tests := []struct {
		name    string
		profile dexco.CapabilityProfile
		want    string
	}{
		{
			name:    "missing id",
			profile: dexco.CapabilityProfile{},
			want:    "id",
		},
		{
			name: "missing user",
			profile: dexco.CapabilityProfile{
				ID:   "sales",
				Mode: dexco.ProfileModeSingleRole,
				Principal: dexco.Principal{
					TenantID: "tenant-1",
				},
			},
			want: "principal.user_id",
		},
		{
			name: "missing tenant",
			profile: dexco.CapabilityProfile{
				ID:   "sales",
				Mode: dexco.ProfileModeSingleRole,
				Principal: dexco.Principal{
					UserID: "user-1",
				},
			},
			want: "principal.tenant_id",
		},
		{
			name: "invalid capability",
			profile: dexco.CapabilityProfile{
				ID:   "sales",
				Mode: dexco.ProfileModeSingleRole,
				Principal: dexco.Principal{
					UserID:       "user-1",
					TenantID:     "tenant-1",
					Capabilities: []string{"sales"},
				},
			},
			want: "principal.capabilities[0]",
		},
		{
			name: "wildcard capability",
			profile: dexco.CapabilityProfile{
				ID:   "sales",
				Mode: dexco.ProfileModeSingleRole,
				Principal: dexco.Principal{
					UserID:       "user-1",
					TenantID:     "tenant-1",
					Capabilities: []string{"reports.*.read"},
				},
			},
			want: "wildcard",
		},
		{
			name: "invalid mode",
			profile: dexco.CapabilityProfile{
				ID:   "sales",
				Mode: dexco.ProfileMode(""),
				Principal: dexco.Principal{
					UserID:   "user-1",
					TenantID: "tenant-1",
				},
			},
			want: "mode",
		},
		{
			name: "active role requires active role",
			profile: dexco.CapabilityProfile{
				ID:   "sales",
				Mode: dexco.ProfileModeActiveRole,
				Principal: dexco.Principal{
					UserID:   "user-1",
					TenantID: "tenant-1",
				},
			},
			want: "principal.active_role",
		},
		{
			name: "combined roles require persona instructions",
			profile: dexco.CapabilityProfile{
				ID:   "management",
				Mode: dexco.ProfileModeCombinedRoles,
				Principal: dexco.Principal{
					UserID:   "user-1",
					TenantID: "tenant-1",
				},
			},
			want: "config.instructions",
		},
		{
			name: "nested runner guardrails",
			profile: func() dexco.CapabilityProfile {
				profile := baseCapabilityProfile(validTool)
				profile.RunnerOptions.Guardrails.ApprovalPolicy = dexco.ApprovalPolicyRequireForAll
				return profile
			}(),
			want: "runner_options.guardrails",
		},
		{
			name: "duplicate visible handlers",
			profile: func() dexco.CapabilityProfile {
				profile := baseCapabilityProfile(duplicateA, duplicateB)
				return profile
			}(),
			want: `duplicate tool name "duplicate_tool"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := dexco.ValidateCapabilityProfile(tt.profile)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateCapabilityProfile() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestProfileConstructionFiltersVisibleTools(t *testing.T) {
	t.Parallel()

	visible := &profileTool{
		name:       "create_sale",
		visibility: dexco.CapabilityRequirement{All: []string{"sales.create"}},
		required:   dexco.CapabilityRequirement{All: []string{"sales.create"}},
	}
	hidden := &profileTool{
		name:       "sensitive_report",
		visibility: dexco.CapabilityRequirement{All: []string{"reports.sensitive.read"}},
		required:   dexco.CapabilityRequirement{All: []string{"reports.sensitive.read"}},
	}
	client := &singlePromptClient{}
	session, err := dexco.NewSessionForProfile(
		context.Background(),
		client,
		baseCapabilityProfile(visible, hidden),
	)
	if err != nil {
		t.Fatalf("NewSessionForProfile() error = %v", err)
	}

	if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "hello"},
	}, dexco.NopSink{}); err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if got := promptToolNames(client.prompt.Tools); !reflect.DeepEqual(got, []string{"create_sale"}) {
		t.Fatalf("prompt tools = %#v, want only create_sale", got)
	}
}

func TestProfileHiddenToolCallDoesNotDispatch(t *testing.T) {
	t.Parallel()

	hidden := &profileTool{
		name:       "sensitive_report",
		visibility: dexco.CapabilityRequirement{All: []string{"reports.sensitive.read"}},
		required:   dexco.CapabilityRequirement{All: []string{"reports.sensitive.read"}},
	}
	client := &profileClient{
		t: t,
		call: dexco.ToolCall{
			CallID:    "hidden-call",
			Name:      "sensitive_report",
			Arguments: json.RawMessage(`{}`),
		},
	}
	session, err := dexco.NewSessionForProfile(
		context.Background(),
		client,
		baseCapabilityProfile(hidden),
	)
	if err != nil {
		t.Fatalf("NewSessionForProfile() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "run hidden"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if hidden.Calls() != 0 {
		t.Fatalf("hidden tool calls = %d, want 0", hidden.Calls())
	}
	if !containsToolResult(result.History, "hidden-call", `unknown tool "sensitive_report"`) {
		t.Fatalf("history missing unknown-tool result: %#v", result.History)
	}
}

func TestProfileMissingRequiredCapabilitiesDeniesBeforeDispatch(t *testing.T) {
	t.Parallel()

	tool := &profileTool{
		name:       "sensitive_report",
		visibility: dexco.CapabilityRequirement{Any: []string{"reports.public.read"}},
		required:   dexco.CapabilityRequirement{All: []string{"reports.sensitive.read"}},
	}
	client := &profileClient{
		t: t,
		call: dexco.ToolCall{
			CallID:    "denied-call",
			Name:      "sensitive_report",
			Arguments: json.RawMessage(`{}`),
		},
	}
	sink := &lockedClientEventSink{}
	session, err := dexco.NewSessionForProfile(
		context.Background(),
		client,
		baseCapabilityProfile(tool),
	)
	if err != nil {
		t.Fatalf("NewSessionForProfile() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "run sensitive report"},
	}, sink)
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if tool.Calls() != 0 {
		t.Fatalf("tool calls = %d, want 0", tool.Calls())
	}
	if !containsToolResult(result.History, "denied-call", "This action is not allowed for the current user.") {
		t.Fatalf("history missing safe denial result: %#v", result.History)
	}
	decision := profilePolicyDecisionEvent(sink.Events())
	if decision == nil {
		t.Fatalf("missing profile policy decision event: %#v", sink.Events())
	}
	if decision.ReasonCode != "missing_capability" {
		t.Fatalf("reason code = %q, want missing_capability", decision.ReasonCode)
	}
	if !reflect.DeepEqual(decision.RequiredCapabilities.All, []string{"reports.sensitive.read"}) {
		t.Fatalf("required capabilities = %#v", decision.RequiredCapabilities)
	}
}

func TestProfileChecksFinalHookMutatedToolCall(t *testing.T) {
	t.Parallel()

	tool := &profileTool{
		name:       "get_report",
		visibility: dexco.CapabilityRequirement{Any: []string{"reports.public.read"}},
		requiredFunc: func(call dexco.ToolCall) (dexco.CapabilityRequirement, error) {
			var args struct {
				Scope string `json:"scope"`
			}
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				return dexco.CapabilityRequirement{}, err
			}
			return dexco.CapabilityRequirement{All: []string{"reports." + args.Scope + ".read"}}, nil
		},
	}
	profile := baseCapabilityProfile(tool)
	profile.RunnerOptions.Hooks.BeforeToolCall = func(ctx context.Context, _ dexco.Turn, call dexco.ToolCall) (dexco.ToolCall, error) {
		if _, ok := dexco.PrincipalFromContext(ctx); !ok {
			t.Fatalf("BeforeToolCall missing principal context")
		}
		call.Arguments = json.RawMessage(`{"scope":"sensitive"}`)
		return call, nil
	}
	client := &profileClient{
		t: t,
		call: dexco.ToolCall{
			CallID:    "report-call",
			Name:      "get_report",
			Arguments: json.RawMessage(`{"scope":"public"}`),
		},
	}
	sink := &lockedClientEventSink{}
	session, err := dexco.NewSessionForProfile(context.Background(), client, profile)
	if err != nil {
		t.Fatalf("NewSessionForProfile() error = %v", err)
	}

	if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "get public report"},
	}, sink); err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if tool.Calls() != 0 {
		t.Fatalf("tool calls = %d, want 0", tool.Calls())
	}
	decision := profilePolicyDecisionEvent(sink.Events())
	if decision == nil {
		t.Fatalf("missing profile policy decision event: %#v", sink.Events())
	}
	if !reflect.DeepEqual(decision.RequiredCapabilities.All, []string{"reports.sensitive.read"}) {
		t.Fatalf("required capabilities = %#v, want reports.sensitive.read", decision.RequiredCapabilities)
	}
}

func TestProfileReviewerCanDenyButNotApproveMissingCapabilities(t *testing.T) {
	t.Parallel()

	t.Run("reviewer denies allowed call", func(t *testing.T) {
		tool := &profileTool{
			name:     "create_sale",
			required: dexco.CapabilityRequirement{All: []string{"sales.create"}},
		}
		profile := baseCapabilityProfile(tool)
		profile.Guardrails.Reviewer = func(ctx context.Context, _ dexco.Turn, request dexco.ToolApprovalRequest) (dexco.ApprovalDecision, error) {
			if _, ok := dexco.PrincipalFromContext(ctx); !ok {
				t.Fatalf("reviewer missing principal context")
			}
			if request.Call.Name != "create_sale" {
				t.Fatalf("reviewed tool = %q, want create_sale", request.Call.Name)
			}
			return dexco.ApprovalDecisionDenied, nil
		}
		client := &profileClient{
			t: t,
			call: dexco.ToolCall{
				CallID:    "review-call",
				Name:      "create_sale",
				Arguments: json.RawMessage(`{}`),
			},
		}
		sink := &lockedClientEventSink{}
		session, err := dexco.NewSessionForProfile(context.Background(), client, profile)
		if err != nil {
			t.Fatalf("NewSessionForProfile() error = %v", err)
		}
		if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: "create sale"},
		}, sink); err != nil {
			t.Fatalf("SubmitUserInput() error = %v", err)
		}
		if tool.Calls() != 0 {
			t.Fatalf("tool calls = %d, want 0", tool.Calls())
		}
		decision := profilePolicyDecisionEvent(sink.Events())
		if decision == nil || decision.ReasonCode != "policy_denied" {
			t.Fatalf("policy decision = %#v, want policy_denied", decision)
		}
	})

	t.Run("reviewer cannot approve missing capability", func(t *testing.T) {
		reviewerCalled := false
		tool := &profileTool{
			name:     "sensitive_report",
			required: dexco.CapabilityRequirement{All: []string{"reports.sensitive.read"}},
		}
		profile := baseCapabilityProfile(tool)
		profile.Guardrails.Reviewer = func(context.Context, dexco.Turn, dexco.ToolApprovalRequest) (dexco.ApprovalDecision, error) {
			reviewerCalled = true
			return dexco.ApprovalDecisionApproved, nil
		}
		client := &profileClient{
			t: t,
			call: dexco.ToolCall{
				CallID:    "missing-call",
				Name:      "sensitive_report",
				Arguments: json.RawMessage(`{}`),
			},
		}
		session, err := dexco.NewSessionForProfile(context.Background(), client, profile)
		if err != nil {
			t.Fatalf("NewSessionForProfile() error = %v", err)
		}
		if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: "get sensitive"},
		}, dexco.NopSink{}); err != nil {
			t.Fatalf("SubmitUserInput() error = %v", err)
		}
		if reviewerCalled {
			t.Fatalf("reviewer was called for missing capability")
		}
		if tool.Calls() != 0 {
			t.Fatalf("tool calls = %d, want 0", tool.Calls())
		}
	})
}

func TestProfileContextHelpersAndDefensiveCopy(t *testing.T) {
	t.Parallel()

	tool := &profileTool{
		name:     "create_sale",
		required: dexco.CapabilityRequirement{All: []string{"sales.create"}},
		contextCheck: func(ctx context.Context) error {
			principal, ok := dexco.PrincipalFromContext(ctx)
			if !ok {
				return errors.New("missing principal")
			}
			if principal.UserID != "user-1" ||
				principal.Metadata["trace_id"] != "original" ||
				!reflect.DeepEqual(principal.Capabilities, []string{"sales.create", "reports.public.read"}) {
				return errors.New("principal context was not defensively copied")
			}
			profile, ok := dexco.CapabilityProfileFromContext(ctx)
			if !ok {
				return errors.New("missing profile")
			}
			if profile.Metadata["surface"] != "chat" {
				return errors.New("profile context was not defensively copied")
			}
			return nil
		},
	}
	profile := baseCapabilityProfile(tool)
	profile.Principal.Metadata = map[string]string{"trace_id": "original"}
	profile.Metadata = map[string]string{"surface": "chat"}
	client := &profileClient{
		t: t,
		call: dexco.ToolCall{
			CallID:    "create-call",
			Name:      "create_sale",
			Arguments: json.RawMessage(`{}`),
		},
	}
	session, err := dexco.NewSessionForProfile(context.Background(), client, profile)
	if err != nil {
		t.Fatalf("NewSessionForProfile() error = %v", err)
	}

	profile.Principal.Capabilities[0] = "sales.delete"
	profile.Principal.Metadata["trace_id"] = "mutated"
	profile.Metadata["surface"] = "admin"

	if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "create sale"},
	}, dexco.NopSink{}); err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}
	if tool.Calls() != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.Calls())
	}
}

func TestProfileDeferredVisibilityFiltersSearchAndDispatch(t *testing.T) {
	t.Parallel()

	visible := deferredProfileTool{
		profileTool: &profileTool{
			name:       "public_report",
			visibility: dexco.CapabilityRequirement{All: []string{"reports.public.read"}},
			required:   dexco.CapabilityRequirement{All: []string{"reports.public.read"}},
		},
		searchText: "public quarterly report",
	}
	hidden := deferredProfileTool{
		profileTool: &profileTool{
			name:       "sensitive_report",
			visibility: dexco.CapabilityRequirement{All: []string{"reports.sensitive.read"}},
			required:   dexco.CapabilityRequirement{All: []string{"reports.sensitive.read"}},
		},
		searchText: "sensitive quarterly report",
	}
	client := &profileSearchClient{t: t, query: "report"}
	session, err := dexco.NewSessionForProfile(
		context.Background(),
		client,
		baseCapabilityProfile(visible, hidden),
	)
	if err != nil {
		t.Fatalf("NewSessionForProfile() error = %v", err)
	}
	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "search tools"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if got := promptToolNames(client.prompts[0].Tools); !reflect.DeepEqual(got, []string{"tool_search"}) {
		t.Fatalf("initial tools = %#v, want only tool_search", got)
	}
	searchOutput := toolResultOutput(result.History, "search-call")
	if !strings.Contains(searchOutput, `"public_report"`) {
		t.Fatalf("tool_search output = %s, want public_report", searchOutput)
	}
	if strings.Contains(searchOutput, "sensitive_report") {
		t.Fatalf("tool_search output leaked hidden tool: %s", searchOutput)
	}
}

func TestProfileDoesNotInjectRawPolicyDataIntoPrompt(t *testing.T) {
	t.Parallel()

	client := &singlePromptClient{}
	profile := baseCapabilityProfile(&profileTool{name: "create_sale"})
	profile.Principal.Roles = []string{"management"}
	profile.Principal.Capabilities = []string{"sales.create", "reports.public.read", "reports.sensitive.read"}
	profile.Metadata = map[string]string{"surface": "internal"}
	session, err := dexco.NewSessionForProfile(context.Background(), client, profile)
	if err != nil {
		t.Fatalf("NewSessionForProfile() error = %v", err)
	}
	if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "hello"},
	}, dexco.NopSink{}); err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	promptText := client.prompt.Instructions + "\n" + strings.Join(client.prompt.DeveloperMessages, "\n")
	for _, item := range client.prompt.History {
		promptText += "\n" + item.Content
	}
	for _, leaked := range []string{"reports.sensitive.read", "management", "internal"} {
		if strings.Contains(promptText, leaked) {
			t.Fatalf("prompt leaked %q in %q", leaked, promptText)
		}
	}
}

func TestProfileProgressNarrationEmitsForSlowToolOnly(t *testing.T) {
	t.Parallel()

	t.Run("slow tool emits safe hint", func(t *testing.T) {
		tool := &profileTool{
			name:     "create_sale",
			required: dexco.CapabilityRequirement{All: []string{"sales.create"}},
			progress: dexco.ProgressHint{Label: "Creating sale", Detail: "Acme renewal"},
			delay:    30 * time.Millisecond,
		}
		profile := baseCapabilityProfile(tool)
		profile.ProgressNarration = dexco.ProgressNarrationConfig{
			Enabled:      true,
			InitialDelay: 5 * time.Millisecond,
		}
		client := &profileClient{
			t: t,
			call: dexco.ToolCall{
				CallID:    "slow-call",
				Name:      "create_sale",
				Arguments: json.RawMessage(`{}`),
			},
		}
		sink := &lockedClientEventSink{}
		session, err := dexco.NewSessionForProfile(context.Background(), client, profile)
		if err != nil {
			t.Fatalf("NewSessionForProfile() error = %v", err)
		}
		if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: "create sale"},
		}, sink); err != nil {
			t.Fatalf("SubmitUserInput() error = %v", err)
		}
		progress := progressEvent(sink.Events())
		if progress == nil {
			t.Fatalf("missing progress event: %#v", sink.Events())
		}
		if progress.Phase != dexco.WorkPhaseRunningTool ||
			progress.Label != "Creating sale" ||
			progress.Detail != "Acme renewal" ||
			progress.ToolName != "create_sale" {
			t.Fatalf("progress = %#v", progress)
		}
	})

	t.Run("quick tool suppresses progress", func(t *testing.T) {
		tool := &profileTool{
			name:     "create_sale",
			required: dexco.CapabilityRequirement{All: []string{"sales.create"}},
			progress: dexco.ProgressHint{Label: "Creating sale"},
		}
		profile := baseCapabilityProfile(tool)
		profile.ProgressNarration = dexco.ProgressNarrationConfig{
			Enabled:      true,
			InitialDelay: 50 * time.Millisecond,
		}
		client := &profileClient{
			t: t,
			call: dexco.ToolCall{
				CallID:    "quick-call",
				Name:      "create_sale",
				Arguments: json.RawMessage(`{}`),
			},
		}
		sink := &lockedClientEventSink{}
		session, err := dexco.NewSessionForProfile(context.Background(), client, profile)
		if err != nil {
			t.Fatalf("NewSessionForProfile() error = %v", err)
		}
		if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: "create sale"},
		}, sink); err != nil {
			t.Fatalf("SubmitUserInput() error = %v", err)
		}
		if progress := progressEvent(sink.Events()); progress != nil {
			t.Fatalf("progress event = %#v, want none", progress)
		}
	})

	t.Run("progress error falls back", func(t *testing.T) {
		tool := &profileTool{
			name:        "create_sale",
			required:    dexco.CapabilityRequirement{All: []string{"sales.create"}},
			progressErr: errors.New("no safe detail"),
			delay:       30 * time.Millisecond,
		}
		profile := baseCapabilityProfile(tool)
		profile.ProgressNarration = dexco.ProgressNarrationConfig{
			Enabled:      true,
			InitialDelay: 5 * time.Millisecond,
		}
		client := &profileClient{
			t: t,
			call: dexco.ToolCall{
				CallID:    "fallback-call",
				Name:      "create_sale",
				Arguments: json.RawMessage(`{}`),
			},
		}
		sink := &lockedClientEventSink{}
		session, err := dexco.NewSessionForProfile(context.Background(), client, profile)
		if err != nil {
			t.Fatalf("NewSessionForProfile() error = %v", err)
		}
		if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: "create sale"},
		}, sink); err != nil {
			t.Fatalf("SubmitUserInput() error = %v", err)
		}
		progress := progressEvent(sink.Events())
		if progress == nil || progress.Label != "Running tool" {
			t.Fatalf("progress = %#v, want Running tool fallback", progress)
		}
	})

	t.Run("slow model start emits waiting", func(t *testing.T) {
		profile := baseCapabilityProfile()
		profile.ProgressNarration = dexco.ProgressNarrationConfig{
			Enabled:      true,
			InitialDelay: 5 * time.Millisecond,
		}
		sink := &lockedClientEventSink{}
		session, err := dexco.NewSessionForProfile(
			context.Background(),
			&slowStartClient{delay: 30 * time.Millisecond},
			profile,
		)
		if err != nil {
			t.Fatalf("NewSessionForProfile() error = %v", err)
		}
		if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: "hello"},
		}, sink); err != nil {
			t.Fatalf("SubmitUserInput() error = %v", err)
		}
		progress := progressEvent(sink.Events())
		if progress == nil || progress.Phase != dexco.WorkPhaseWaitingForModel {
			t.Fatalf("progress = %#v, want waiting_for_model", progress)
		}
	})

	t.Run("slow policy review emits checking access", func(t *testing.T) {
		tool := &profileTool{
			name:     "create_sale",
			required: dexco.CapabilityRequirement{All: []string{"sales.create"}},
			progress: dexco.ProgressHint{Label: "Creating sale"},
		}
		profile := baseCapabilityProfile(tool)
		profile.ProgressNarration = dexco.ProgressNarrationConfig{
			Enabled:      true,
			InitialDelay: 5 * time.Millisecond,
		}
		profile.Guardrails.Reviewer = func(ctx context.Context, _ dexco.Turn, _ dexco.ToolApprovalRequest) (dexco.ApprovalDecision, error) {
			timer := time.NewTimer(30 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return dexco.ApprovalDecisionDenied, ctx.Err()
			case <-timer.C:
				return dexco.ApprovalDecisionApproved, nil
			}
		}
		client := &profileClient{
			t: t,
			call: dexco.ToolCall{
				CallID:    "policy-call",
				Name:      "create_sale",
				Arguments: json.RawMessage(`{}`),
			},
		}
		sink := &lockedClientEventSink{}
		session, err := dexco.NewSessionForProfile(context.Background(), client, profile)
		if err != nil {
			t.Fatalf("NewSessionForProfile() error = %v", err)
		}
		if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: "create sale"},
		}, sink); err != nil {
			t.Fatalf("SubmitUserInput() error = %v", err)
		}
		progress := progressEvent(sink.Events())
		if progress == nil || progress.Phase != dexco.WorkPhaseCheckingPolicy || progress.Label != "Checking access" {
			t.Fatalf("progress = %#v, want checking access", progress)
		}
	})
}

func TestProfileParallelProgressNarrationAggregates(t *testing.T) {
	t.Parallel()

	toolA := &profileTool{
		name:     "task_a",
		required: dexco.CapabilityRequirement{All: []string{"sales.create"}},
		progress: dexco.ProgressHint{Label: "Creating sale"},
		delay:    100 * time.Millisecond,
		parallel: true,
	}
	toolB := &profileTool{
		name:     "task_b",
		required: dexco.CapabilityRequirement{All: []string{"sales.create"}},
		progress: dexco.ProgressHint{Label: "Reading report"},
		delay:    100 * time.Millisecond,
		parallel: true,
	}
	profile := baseCapabilityProfile(toolA, toolB)
	profile.RunnerOptions.ParallelTools = true
	profile.ProgressNarration = dexco.ProgressNarrationConfig{
		Enabled:      true,
		InitialDelay: 10 * time.Millisecond,
	}
	client := &profileParallelClient{
		t: t,
		calls: []dexco.ToolCall{
			{CallID: "call-a", Name: "task_a", Arguments: json.RawMessage(`{}`)},
			{CallID: "call-b", Name: "task_b", Arguments: json.RawMessage(`{}`)},
		},
	}
	sink := &lockedClientEventSink{}
	session, err := dexco.NewSessionForProfile(context.Background(), client, profile)
	if err != nil {
		t.Fatalf("NewSessionForProfile() error = %v", err)
	}
	if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "run tasks"},
	}, sink); err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	progress := progressEventByPhase(sink.Events(), dexco.WorkPhaseWaitingParallel)
	if progress == nil {
		t.Fatalf("missing aggregate progress event: %#v", sink.Events())
	}
	if progress.Phase != dexco.WorkPhaseWaitingParallel || progress.Message != "Running 2 tasks" {
		t.Fatalf("progress = %#v, want aggregate Running 2 tasks", progress)
	}
}

func TestWorkflowHelpersExposeExplicitChatAndCodingProfiles(t *testing.T) {
	t.Parallel()

	chatNames := profileHandlerNames(dexco.ChatWorkflowProfileHandlers(nil))
	if !reflect.DeepEqual(chatNames, []string{"current_time", "request_user_input"}) {
		t.Fatalf("chat profile handlers = %#v", chatNames)
	}
	codingNames := profileHandlerNames(dexco.CodingWorkflowProfileHandlers(nil))
	wantCoding := []string{"exec_command", "current_time", "request_user_input", "update_plan", "view_image"}
	if !reflect.DeepEqual(codingNames, wantCoding) {
		t.Fatalf("coding profile handlers = %#v, want %#v", codingNames, wantCoding)
	}
	if strings.Contains(strings.Join(codingNames, ","), "request_permissions") {
		t.Fatalf("coding profile handlers unexpectedly include request_permissions: %#v", codingNames)
	}
}

func profilePolicyDecisionEvent(events []dexco.ClientEvent) *dexco.ToolPolicyDecision {
	for _, event := range events {
		if event.Type == dexco.ClientEventToolApprovalDecision && event.ToolPolicyDecision != nil {
			decision := *event.ToolPolicyDecision
			return &decision
		}
	}
	return nil
}

func progressEvent(events []dexco.ClientEvent) *dexco.ProgressNarration {
	return progressEventByPhase(events, "")
}

func progressEventByPhase(events []dexco.ClientEvent, phase dexco.WorkPhase) *dexco.ProgressNarration {
	for _, event := range events {
		if event.Type == dexco.ClientEventProgressNarration && event.ProgressNarration != nil {
			if phase != "" && event.ProgressNarration.Phase != phase {
				continue
			}
			progress := *event.ProgressNarration
			return &progress
		}
	}
	return nil
}

func toolResultOutput(history []dexco.Item, callID string) string {
	for _, item := range history {
		if item.ToolResult != nil && item.ToolResult.CallID == callID {
			return item.ToolResult.Output
		}
	}
	return ""
}

func profileHandlerNames(handlers []dexco.ProfileHandler) []string {
	names := make([]string, 0, len(handlers))
	for _, handler := range handlers {
		names = append(names, handler.Name())
	}
	return names
}
