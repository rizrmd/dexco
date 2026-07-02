package dexco

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/rizrmd/dexco/internal/tools/builtin"
)

type Principal struct {
	UserID               string
	TenantID             string
	Roles                []string
	Capabilities         []string
	ActiveRole           string
	CapabilitySetVersion string
	Metadata             map[string]string
}

type ProfileMode string

const (
	ProfileModeSingleRole    ProfileMode = "single_role"
	ProfileModeActiveRole    ProfileMode = "active_role"
	ProfileModeCombinedRoles ProfileMode = "combined_roles"
)

type CapabilityProfile struct {
	ID                string
	Description       string
	Principal         Principal
	Mode              ProfileMode
	Config            Config
	Handlers          []ProfileHandler
	RunnerOptions     RunnerOptions
	Guardrails        Guardrails
	ProgressNarration ProgressNarrationConfig
	Metadata          map[string]string
}

type ProfileHandler interface {
	Handler
	Visibility(ctx context.Context, principal Principal) (CapabilityRequirement, error)
	RequiredCapabilities(ctx context.Context, call ToolCall) (CapabilityRequirement, error)
	Progress(ctx context.Context, call ToolCall) (ProgressHint, error)
}

type DeferredProfileHandler interface {
	ProfileHandler
	DeferredSearchText() string
}

type HandlerProfilePolicy struct {
	Visibility           CapabilityRequirement
	RequiredCapabilities CapabilityRequirement
	Progress             ProgressHint
}

type ProfileRequest struct {
	UserID       string
	TenantID     string
	ActiveRole   string
	ProductArea  string
	Conversation string
	Metadata     map[string]string
}

type ProfileResolver interface {
	ResolveProfile(ctx context.Context, request ProfileRequest) (CapabilityProfile, error)
}

type capabilityProfileContextKey struct{}
type principalContextKey struct{}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	if !ok {
		return Principal{}, false
	}
	return clonePrincipal(principal), true
}

func CapabilityProfileFromContext(ctx context.Context) (CapabilityProfile, bool) {
	if ctx == nil {
		return CapabilityProfile{}, false
	}
	profile, ok := ctx.Value(capabilityProfileContextKey{}).(CapabilityProfile)
	if !ok {
		return CapabilityProfile{}, false
	}
	return cloneCapabilityProfile(profile), true
}

func NewProfileHandler(handler Handler, policy HandlerProfilePolicy) ProfileHandler {
	return staticProfileHandler{
		handler: handler,
		policy:  cloneHandlerProfilePolicy(policy),
	}
}

func ChatWorkflowProfileHandlers(responder UserInputResponder) []ProfileHandler {
	return []ProfileHandler{
		NewProfileHandler(builtin.CurrentTimeHandler{}, HandlerProfilePolicy{
			Progress: ProgressHint{Label: "Checking time"},
		}),
		NewProfileHandler(builtin.RequestUserInputHandler{Responder: responder}, HandlerProfilePolicy{
			Progress: ProgressHint{Label: "Asking for input"},
		}),
	}
}

func CodingWorkflowProfileHandlers(responder UserInputResponder) []ProfileHandler {
	return []ProfileHandler{
		NewProfileHandler(builtin.ExecCommandHandler{}, HandlerProfilePolicy{
			Progress: ProgressHint{Label: "Running command"},
		}),
		NewProfileHandler(builtin.CurrentTimeHandler{}, HandlerProfilePolicy{
			Progress: ProgressHint{Label: "Checking time"},
		}),
		NewProfileHandler(builtin.RequestUserInputHandler{Responder: responder}, HandlerProfilePolicy{
			Progress: ProgressHint{Label: "Asking for input"},
		}),
		NewProfileHandler(builtin.UpdatePlanHandler{}, HandlerProfilePolicy{
			Progress: ProgressHint{Label: "Updating plan"},
		}),
		NewProfileHandler(builtin.ViewImageHandler{}, HandlerProfilePolicy{
			Progress: ProgressHint{Label: "Loading image"},
		}),
	}
}

func ValidateCapabilityProfile(profile CapabilityProfile) error {
	snapshot := cloneCapabilityProfile(profile)
	_, err := validateCapabilityProfile(context.Background(), snapshot)
	return err
}

func NewSessionForProfile(
	ctx context.Context,
	modelClient ModelClient,
	profile CapabilityProfile,
) (*Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	snapshot := cloneCapabilityProfile(profile)
	visible, err := validateCapabilityProfile(ctx, snapshot)
	if err != nil {
		return nil, err
	}

	router, err := NewRouter()
	if err != nil {
		return nil, err
	}
	for _, handler := range visible {
		wrapped := profileDispatchHandler{
			profile: snapshot,
			handler: handler,
		}
		if deferred, ok := handler.(DeferredProfileHandler); ok {
			if err := router.RegisterDeferred(wrapped, deferred.DeferredSearchText()); err != nil {
				return nil, fmt.Errorf("new session for profile: register deferred handler %q: %w", handler.Name(), err)
			}
			continue
		}
		if err := router.Register(wrapped); err != nil {
			return nil, fmt.Errorf("new session for profile: register handler %q: %w", handler.Name(), err)
		}
	}

	options := snapshot.RunnerOptions
	options.Guardrails = profileGuardrails(snapshot.Guardrails)
	if options.Guardrails.Reviewer != nil {
		reviewer := options.Guardrails.Reviewer
		options.Guardrails.Reviewer = func(ctx context.Context, turn Turn, request ToolApprovalRequest) (ApprovalDecision, error) {
			return reviewer(contextWithCapabilityProfile(ctx, snapshot), turn, request)
		}
	}
	options.Hooks = profileHooks(snapshot, options.Hooks)
	options.progressNarration = snapshot.ProgressNarration
	turnRunner, err := NewRunnerWithOptions(modelClient, router, options)
	if err != nil {
		return nil, err
	}
	return NewSession(snapshot.Config, turnRunner)
}

func validateCapabilityProfile(
	ctx context.Context,
	profile CapabilityProfile,
) ([]ProfileHandler, error) {
	if !safeLogIdentifier(profile.ID) {
		return nil, fmt.Errorf("validate capability profile: id: must be non-empty and safe for logs")
	}
	if strings.TrimSpace(profile.Principal.UserID) == "" {
		return nil, fmt.Errorf("validate capability profile: principal.user_id: required")
	}
	if strings.TrimSpace(profile.Principal.TenantID) == "" {
		return nil, fmt.Errorf("validate capability profile: principal.tenant_id: required")
	}
	if err := validateProfileMode(profile); err != nil {
		return nil, err
	}
	if err := validateCapabilityList("principal.capabilities", profile.Principal.Capabilities); err != nil {
		return nil, err
	}
	if err := validateMetadata("principal.metadata", profile.Principal.Metadata); err != nil {
		return nil, err
	}
	if err := validateMetadata("metadata", profile.Metadata); err != nil {
		return nil, err
	}
	if !guardrailsEmpty(profile.RunnerOptions.Guardrails) {
		return nil, fmt.Errorf("validate capability profile: runner_options.guardrails: profile.guardrails is the only guardrail source")
	}
	if profile.ProgressNarration.Enabled {
		if profile.ProgressNarration.InitialDelay <= 0 {
			return nil, fmt.Errorf("validate capability profile: progress_narration.initial_delay: must be greater than zero when enabled")
		}
		if profile.ProgressNarration.RepeatInterval > 0 &&
			profile.ProgressNarration.RepeatInterval < profile.ProgressNarration.InitialDelay {
			return nil, fmt.Errorf("validate capability profile: progress_narration.repeat_interval: must be greater than or equal to initial_delay")
		}
	}

	return visibleProfileHandlers(ctx, profile)
}

func validateProfileMode(profile CapabilityProfile) error {
	switch profile.Mode {
	case ProfileModeSingleRole:
		if len(profile.Principal.Roles) > 1 {
			return fmt.Errorf("validate capability profile: mode: single_role requires zero or one role")
		}
	case ProfileModeActiveRole:
		if strings.TrimSpace(profile.Principal.ActiveRole) == "" {
			return fmt.Errorf("validate capability profile: principal.active_role: required for active_role mode")
		}
	case ProfileModeCombinedRoles:
		if strings.TrimSpace(profile.Config.Instructions) == "" {
			return fmt.Errorf("validate capability profile: config.instructions: required for combined_roles mode")
		}
	default:
		return fmt.Errorf("validate capability profile: mode: invalid profile mode %q", profile.Mode)
	}
	return nil
}

func visibleProfileHandlers(ctx context.Context, profile CapabilityProfile) ([]ProfileHandler, error) {
	visible := make([]ProfileHandler, 0, len(profile.Handlers))
	seenNames := make(map[string]struct{})
	profileCtx := contextWithCapabilityProfile(ctx, profile)
	for i, handler := range profile.Handlers {
		if handler == nil {
			return nil, fmt.Errorf("validate capability profile: handlers[%d]: nil handler", i)
		}
		requirement, err := handler.Visibility(profileCtx, clonePrincipal(profile.Principal))
		if err != nil {
			return nil, fmt.Errorf("validate capability profile: handlers[%d].visibility: %w", i, err)
		}
		requirement, err = normalizedCapabilityRequirement(
			fmt.Sprintf("handlers[%d].visibility", i),
			requirement,
		)
		if err != nil {
			return nil, err
		}
		if !capabilityRequirementSatisfied(profile.Principal.Capabilities, requirement) {
			continue
		}
		name := handler.Name()
		if name == "" {
			return nil, fmt.Errorf("validate capability profile: handlers[%d]: empty tool name", i)
		}
		if _, exists := seenNames[name]; exists {
			return nil, fmt.Errorf("validate capability profile: handlers: duplicate tool name %q", name)
		}
		seenNames[name] = struct{}{}
		if err := validateToolSpec(fmt.Sprintf("handlers[%d].spec", i), name, handler.Spec()); err != nil {
			return nil, err
		}
		visible = append(visible, handler)
	}
	return visible, nil
}

func validateToolSpec(field string, handlerName string, spec ToolSpec) error {
	if spec.Name == "" {
		return fmt.Errorf("validate capability profile: %s.name: required", field)
	}
	if spec.Name != handlerName {
		return fmt.Errorf("validate capability profile: %s.name: got %q, want handler name %q", field, spec.Name, handlerName)
	}
	if !safeToolName(spec.Name) {
		return fmt.Errorf("validate capability profile: %s.name: unsafe tool name %q", field, spec.Name)
	}
	return nil
}

func validateCapabilityList(field string, capabilities []string) error {
	for i, capability := range capabilities {
		if err := validateCapabilityKey(capability); err != nil {
			return fmt.Errorf("validate capability profile: %s[%d]: %w", field, i, err)
		}
	}
	return nil
}

func normalizedCapabilityRequirement(
	field string,
	requirement CapabilityRequirement,
) (CapabilityRequirement, error) {
	normalized := CapabilityRequirement{
		All: append([]string(nil), requirement.All...),
		Any: append([]string(nil), requirement.Any...),
	}
	if err := validateCapabilityList(field+".all", normalized.All); err != nil {
		return CapabilityRequirement{}, err
	}
	if err := validateCapabilityList(field+".any", normalized.Any); err != nil {
		return CapabilityRequirement{}, err
	}
	return normalized, nil
}

func validateCapabilityKey(capability string) error {
	if strings.Contains(capability, "*") {
		return fmt.Errorf("wildcard not supported")
	}
	parts := strings.Split(capability, ".")
	if len(parts) != 2 && len(parts) != 3 {
		return fmt.Errorf("must contain exactly two or three dot-separated parts")
	}
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("contains an empty part")
		}
		for _, r := range part {
			if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-') {
				return fmt.Errorf("contains invalid character %q", r)
			}
		}
	}
	return nil
}

func capabilityRequirementSatisfied(capabilities []string, requirement CapabilityRequirement) bool {
	set := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		set[capability] = struct{}{}
	}
	for _, capability := range requirement.All {
		if _, ok := set[capability]; !ok {
			return false
		}
	}
	if len(requirement.Any) == 0 {
		return true
	}
	for _, capability := range requirement.Any {
		if _, ok := set[capability]; ok {
			return true
		}
	}
	return false
}

func guardrailsEmpty(guardrails Guardrails) bool {
	return guardrails.ApprovalPolicy == "" &&
		guardrails.Reviewer == nil &&
		guardrails.PermissionGrants == nil
}

func profileGuardrails(guardrails Guardrails) Guardrails {
	if guardrails.Reviewer != nil &&
		(guardrails.ApprovalPolicy == "" || guardrails.ApprovalPolicy == ApprovalPolicyAllowAll) {
		guardrails.ApprovalPolicy = ApprovalPolicyRequireForAll
	}
	return guardrails
}

func profileHooks(profile CapabilityProfile, hooks Hooks) Hooks {
	wrapped := hooks
	if hooks.BeforeModelRequest != nil {
		wrapped.BeforeModelRequest = func(ctx context.Context, turn Turn, prompt Prompt) (Prompt, error) {
			return hooks.BeforeModelRequest(contextWithCapabilityProfile(ctx, profile), turn, prompt)
		}
	}
	if hooks.AfterModelRequest != nil {
		wrapped.AfterModelRequest = func(ctx context.Context, turn Turn, prompt Prompt, err error) error {
			return hooks.AfterModelRequest(contextWithCapabilityProfile(ctx, profile), turn, prompt, err)
		}
	}
	if hooks.BeforeToolCall != nil {
		wrapped.BeforeToolCall = func(ctx context.Context, turn Turn, call ToolCall) (ToolCall, error) {
			return hooks.BeforeToolCall(contextWithCapabilityProfile(ctx, profile), turn, call)
		}
	}
	if hooks.AfterToolCall != nil {
		wrapped.AfterToolCall = func(ctx context.Context, turn Turn, call ToolCall, item Item) (Item, error) {
			return hooks.AfterToolCall(contextWithCapabilityProfile(ctx, profile), turn, call, item)
		}
	}
	if hooks.ToolLifecycle != nil {
		wrapped.ToolLifecycle = func(ctx context.Context, turn Turn, event ToolLifecycleEvent) error {
			return hooks.ToolLifecycle(contextWithCapabilityProfile(ctx, profile), turn, event)
		}
	}
	if hooks.ReviewToolCall != nil {
		wrapped.ReviewToolCall = func(ctx context.Context, turn Turn, request ToolApprovalRequest) (ApprovalDecision, error) {
			return hooks.ReviewToolCall(contextWithCapabilityProfile(ctx, profile), turn, request)
		}
	}
	return wrapped
}

type staticProfileHandler struct {
	handler Handler
	policy  HandlerProfilePolicy
}

func (h staticProfileHandler) Name() string {
	if h.handler == nil {
		return ""
	}
	return h.handler.Name()
}

func (h staticProfileHandler) Spec() ToolSpec {
	if h.handler == nil {
		return ToolSpec{}
	}
	return h.handler.Spec()
}

func (h staticProfileHandler) Call(ctx context.Context, call ToolCall) (ToolResult, error) {
	if h.handler == nil {
		return ToolResult{}, fmt.Errorf("nil handler")
	}
	return h.handler.Call(ctx, call)
}

func (h staticProfileHandler) Visibility(context.Context, Principal) (CapabilityRequirement, error) {
	return cloneCapabilityRequirement(h.policy.Visibility), nil
}

func (h staticProfileHandler) RequiredCapabilities(context.Context, ToolCall) (CapabilityRequirement, error) {
	return cloneCapabilityRequirement(h.policy.RequiredCapabilities), nil
}

func (h staticProfileHandler) Progress(context.Context, ToolCall) (ProgressHint, error) {
	return h.policy.Progress, nil
}

func (h staticProfileHandler) Guardrail(ctx context.Context, call ToolCall) (ToolGuardrail, error) {
	guarded, ok := h.handler.(GuardedHandler)
	if !ok {
		return ToolGuardrail{
			Risk:                ToolRiskUnknown,
			ApprovalRequirement: ApprovalRequirementNone,
		}, nil
	}
	return guarded.Guardrail(ctx, call)
}

func (h staticProfileHandler) SupportsParallel() bool {
	parallel, ok := h.handler.(ParallelHandler)
	return ok && parallel.SupportsParallel()
}

func (h staticProfileHandler) InterruptsOnPendingInput() bool {
	interrupt, ok := h.handler.(PendingInputInterruptHandler)
	return ok && interrupt.InterruptsOnPendingInput()
}

type profileDispatchHandler struct {
	profile CapabilityProfile
	handler ProfileHandler
}

func (h profileDispatchHandler) Name() string {
	return h.handler.Name()
}

func (h profileDispatchHandler) Spec() ToolSpec {
	return h.handler.Spec()
}

func (h profileDispatchHandler) Call(ctx context.Context, call ToolCall) (ToolResult, error) {
	return h.handler.Call(contextWithCapabilityProfile(ctx, h.profile), call)
}

func (h profileDispatchHandler) Guardrail(ctx context.Context, call ToolCall) (ToolGuardrail, error) {
	profileCtx := contextWithCapabilityProfile(ctx, h.profile)
	requirement, err := h.handler.RequiredCapabilities(profileCtx, call)
	if err != nil {
		return h.deniedGuardrail(call, CapabilityRequirement{}, "invalid_scope"), nil
	}
	requirement, err = normalizedCapabilityRequirement("required_capabilities", requirement)
	if err != nil {
		return h.deniedGuardrail(call, CapabilityRequirement{}, "invalid_scope"), nil
	}
	if !capabilityRequirementSatisfied(h.profile.Principal.Capabilities, requirement) {
		return h.deniedGuardrail(call, requirement, "missing_capability"), nil
	}

	guardrail := ToolGuardrail{
		Risk:                ToolRiskUnknown,
		ApprovalRequirement: ApprovalRequirementNone,
	}
	if guarded, ok := h.handler.(GuardedHandler); ok {
		guardrail, err = guarded.Guardrail(profileCtx, call)
		if err != nil {
			return ToolGuardrail{}, err
		}
	}
	reasonCode := ""
	if guardrail.ApprovalRequirement == ApprovalRequirementDenied {
		reasonCode = "handler_denied"
	}
	guardrail.ToolPolicyDecision = &ToolPolicyDecision{
		ToolName:             call.Name,
		Decision:             ApprovalDecisionApproved,
		ReasonCode:           reasonCode,
		RequiredCapabilities: cloneCapabilityRequirement(requirement),
	}
	progress, err := h.handler.Progress(profileCtx, call)
	if err != nil || strings.TrimSpace(progress.Label) == "" {
		progress = ProgressHint{Label: "Running tool"}
	}
	progress = ProgressHint{
		Label:  strings.TrimSpace(progress.Label),
		Detail: strings.TrimSpace(progress.Detail),
	}
	guardrail.ProgressHint = &progress
	return guardrail, nil
}

func (h profileDispatchHandler) SupportsParallel() bool {
	parallel, ok := h.handler.(ParallelHandler)
	return ok && parallel.SupportsParallel()
}

func (h profileDispatchHandler) InterruptsOnPendingInput() bool {
	interrupt, ok := h.handler.(PendingInputInterruptHandler)
	return ok && interrupt.InterruptsOnPendingInput()
}

func (h profileDispatchHandler) deniedGuardrail(
	call ToolCall,
	requirement CapabilityRequirement,
	reasonCode string,
) ToolGuardrail {
	return ToolGuardrail{
		Risk:                ToolRiskUnknown,
		ApprovalRequirement: ApprovalRequirementDenied,
		Reason:              "profile policy denied tool call",
		ToolPolicyDecision: &ToolPolicyDecision{
			ToolName:             call.Name,
			Decision:             ApprovalDecisionDenied,
			ReasonCode:           reasonCode,
			RequiredCapabilities: cloneCapabilityRequirement(requirement),
		},
	}
}

func contextWithCapabilityProfile(ctx context.Context, profile CapabilityProfile) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	snapshot := cloneCapabilityProfile(profile)
	ctx = context.WithValue(ctx, capabilityProfileContextKey{}, snapshot)
	ctx = context.WithValue(ctx, principalContextKey{}, snapshot.Principal)
	return ctx
}

func cloneCapabilityProfile(profile CapabilityProfile) CapabilityProfile {
	profile.Principal = clonePrincipal(profile.Principal)
	profile.Config = cloneProfileConfig(profile.Config)
	profile.Handlers = append([]ProfileHandler(nil), profile.Handlers...)
	profile.Metadata = cloneStringMap(profile.Metadata)
	return profile
}

func cloneProfileConfig(cfg Config) Config {
	cfg.WebSearch = cloneProfileWebSearchRequest(cfg.WebSearch)
	if cfg.ContextInstructions.Snapshot != nil {
		snapshot := *cfg.ContextInstructions.Snapshot
		snapshot.Sources = append([]InstructionSource(nil), snapshot.Sources...)
		cfg.ContextInstructions.Snapshot = &snapshot
	}
	if cfg.EnvironmentContext.Snapshot != nil {
		snapshot := *cfg.EnvironmentContext.Snapshot
		snapshot.Environments = append([]EnvironmentState(nil), snapshot.Environments...)
		cfg.EnvironmentContext.Snapshot = &snapshot
	}
	cfg.RolloutBudget.ReminderAtRemainingTokens = append(
		[]int64(nil),
		cfg.RolloutBudget.ReminderAtRemainingTokens...,
	)
	return cfg
}

func cloneProfileWebSearchRequest(request *WebSearchRequest) *WebSearchRequest {
	if request == nil {
		return nil
	}
	cloned := *request
	cloned.AllowedDomains = append([]string(nil), request.AllowedDomains...)
	if request.UserLocation != nil {
		location := *request.UserLocation
		cloned.UserLocation = &location
	}
	return &cloned
}

func clonePrincipal(principal Principal) Principal {
	principal.Roles = append([]string(nil), principal.Roles...)
	principal.Capabilities = dedupeStrings(principal.Capabilities)
	principal.Metadata = cloneStringMap(principal.Metadata)
	return principal
}

func cloneHandlerProfilePolicy(policy HandlerProfilePolicy) HandlerProfilePolicy {
	policy.Visibility = cloneCapabilityRequirement(policy.Visibility)
	policy.RequiredCapabilities = cloneCapabilityRequirement(policy.RequiredCapabilities)
	return policy
}

func cloneCapabilityRequirement(requirement CapabilityRequirement) CapabilityRequirement {
	return CapabilityRequirement{
		All: append([]string(nil), requirement.All...),
		Any: append([]string(nil), requirement.Any...),
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	deduped := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		deduped = append(deduped, value)
	}
	return deduped
}

func safeLogIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' || r == ':') {
			return false
		}
	}
	return true
}

func safeToolName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func validateMetadata(field string, metadata map[string]string) error {
	if len(metadata) == 0 {
		return nil
	}
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("validate capability profile: %s: empty metadata key", field)
		}
		if metadataKeyLooksSecret(key) {
			return fmt.Errorf("validate capability profile: %s.%s: metadata must not contain secrets", field, key)
		}
		if containsControlRune(key) || containsControlRune(metadata[key]) {
			return fmt.Errorf("validate capability profile: %s.%s: metadata must be safe for logs", field, key)
		}
	}
	return nil
}

func metadataKeyLooksSecret(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, marker := range []string{"secret", "password", "credential", "api_key", "apikey", "access_token", "refresh_token"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func containsControlRune(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
