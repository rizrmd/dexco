package dexco

import (
	"context"
	"fmt"
	"time"

	"github.com/rizrmd/dexco/internal/model"
	permissionstore "github.com/rizrmd/dexco/internal/permissions"
	"github.com/rizrmd/dexco/internal/runner"
	"github.com/rizrmd/dexco/internal/session"
	"github.com/rizrmd/dexco/internal/tools"
	"github.com/rizrmd/dexco/internal/tools/builtin"
)

type ItemKind = model.ItemKind

const (
	ItemContext          = model.ItemContext
	ItemUserInput        = model.ItemUserInput
	ItemAssistantMessage = model.ItemAssistantMessage
	ItemReasoning        = model.ItemReasoning
	ItemToolCall         = model.ItemToolCall
	ItemToolResult       = model.ItemToolResult
	ItemWebSearch        = model.ItemWebSearch
	ItemHookPrompt       = model.ItemHookPrompt
	ItemImageGeneration  = model.ItemImageGeneration
)

type Item = model.Item
type ToolCall = model.ToolCall
type ToolResult = model.ToolResult
type ToolLifecycleEvent = model.ToolLifecycleEvent
type ToolLifecyclePhase = model.ToolLifecyclePhase
type ToolLifecycleOutcome = model.ToolLifecycleOutcome
type PlanStepStatus = model.PlanStepStatus
type PlanStep = model.PlanStep
type PlanUpdate = model.PlanUpdate
type WebSearch = model.WebSearch
type WebSearchAction = model.WebSearchAction
type WebSearchActionKind = model.WebSearchActionKind
type WebSearchMode = model.WebSearchMode
type WebSearchRequest = model.WebSearchRequest
type WebSearchUserLocation = model.WebSearchUserLocation
type ImageGeneration = model.ImageGeneration
type HookPrompt = model.HookPrompt
type HookPromptFragment = model.HookPromptFragment

const (
	PlanStepPending    = model.PlanStepPending
	PlanStepInProgress = model.PlanStepInProgress
	PlanStepCompleted  = model.PlanStepCompleted
)

const (
	ToolLifecycleStart  = model.ToolLifecycleStart
	ToolLifecycleFinish = model.ToolLifecycleFinish
)

const (
	ToolLifecycleOutcomeCompleted = model.ToolLifecycleOutcomeCompleted
	ToolLifecycleOutcomeFailed    = model.ToolLifecycleOutcomeFailed
)

const (
	WebSearchActionOther      = model.WebSearchActionOther
	WebSearchActionSearch     = model.WebSearchActionSearch
	WebSearchActionOpenPage   = model.WebSearchActionOpenPage
	WebSearchActionFindInPage = model.WebSearchActionFindInPage
)

const (
	WebSearchModeDisabled = model.WebSearchModeDisabled
	WebSearchModeCached   = model.WebSearchModeCached
	WebSearchModeLive     = model.WebSearchModeLive
	WebSearchModeIndexed  = model.WebSearchModeIndexed
)

type ContentPart = model.ContentPart
type ContentPartKind = model.ContentPartKind
type MemoryCitation = model.MemoryCitation
type MemoryCitationEntry = model.MemoryCitationEntry

const (
	ContentPartText      = model.ContentPartText
	ContentPartImage     = model.ContentPartImage
	ContentPartEncrypted = model.ContentPartEncrypted

	RemoteImageURLToolResultError = model.RemoteImageURLToolResultError
)

type ToolSpec = model.ToolSpec
type ModelErrorKind = model.ModelErrorKind

const (
	ModelErrorUnknown     = model.ModelErrorUnknown
	ModelErrorCyberPolicy = model.ModelErrorCyberPolicy
	ModelErrorQuota       = model.ModelErrorQuota
)

type ModelError = model.ModelError
type ToolRisk = model.ToolRisk

const (
	ToolRiskUnknown          = model.ToolRiskUnknown
	ToolRiskReadOnly         = model.ToolRiskReadOnly
	ToolRiskUserInteraction  = model.ToolRiskUserInteraction
	ToolRiskCommandExecution = model.ToolRiskCommandExecution
	ToolRiskWorkspaceWrite   = model.ToolRiskWorkspaceWrite
	ToolRiskNetwork          = model.ToolRiskNetwork
	ToolRiskDestructive      = model.ToolRiskDestructive
)

type ApprovalRequirement = model.ApprovalRequirement

const (
	ApprovalRequirementNone     = model.ApprovalRequirementNone
	ApprovalRequirementRequired = model.ApprovalRequirementRequired
	ApprovalRequirementDenied   = model.ApprovalRequirementDenied
)

type ApprovalPolicy = model.ApprovalPolicy

const (
	ApprovalPolicyAllowAll            = model.ApprovalPolicyAllowAll
	ApprovalPolicyRequireForSensitive = model.ApprovalPolicyRequireForSensitive
	ApprovalPolicyRequireForAll       = model.ApprovalPolicyRequireForAll
	ApprovalPolicyDenyAll             = model.ApprovalPolicyDenyAll
)

type ApprovalDecision = model.ApprovalDecision

const (
	ApprovalDecisionNoDecision = model.ApprovalDecisionNoDecision
	ApprovalDecisionApproved   = model.ApprovalDecisionApproved
	ApprovalDecisionDenied     = model.ApprovalDecisionDenied
)

type ToolGuardrail = model.ToolGuardrail
type ToolApprovalRequest = model.ToolApprovalRequest
type PermissionGrantScope = model.PermissionGrantScope
type PermissionGrant = model.PermissionGrant
type CapabilityRequirement = model.CapabilityRequirement
type ToolPolicyDecision = model.ToolPolicyDecision
type WorkPhase = model.WorkPhase
type ActiveWork = model.ActiveWork
type ProgressNarrationConfig = model.ProgressNarrationConfig
type ProgressHint = model.ProgressHint
type ProgressNarration = model.ProgressNarration

const (
	PermissionGrantScopeTurn    = model.PermissionGrantScopeTurn
	PermissionGrantScopeSession = model.PermissionGrantScopeSession
)

const (
	WorkPhaseWaitingForModel = model.WorkPhaseWaitingForModel
	WorkPhaseCheckingPolicy  = model.WorkPhaseCheckingPolicy
	WorkPhaseRunningTool     = model.WorkPhaseRunningTool
	WorkPhaseRetryingTool    = model.WorkPhaseRetryingTool
	WorkPhaseWaitingParallel = model.WorkPhaseWaitingParallel
	WorkPhaseGeneratingReply = model.WorkPhaseGeneratingReply
)

type PermissionGrantStore = permissionstore.Store

func NewPermissionGrantStore() *PermissionGrantStore {
	return permissionstore.NewStore()
}

type Prompt = model.Prompt
type UserInput = model.UserInput
type AdditionalContextKind = model.AdditionalContextKind

const (
	AdditionalContextUntrusted   = model.AdditionalContextUntrusted
	AdditionalContextApplication = model.AdditionalContextApplication
)

type AdditionalContextEntry = model.AdditionalContextEntry
type OpUserInput = model.OpUserInput
type Turn = model.Turn
type TurnStatus = model.TurnStatus

const (
	TurnStatusRunning   = model.TurnStatusRunning
	TurnStatusCompleted = model.TurnStatusCompleted
	TurnStatusFailed    = model.TurnStatusFailed
)

type ResponseEventType = model.ResponseEventType

const (
	EventCreated                   = model.EventCreated
	EventOutputItemAdded           = model.EventOutputItemAdded
	EventOutputTextDelta           = model.EventOutputTextDelta
	EventReasoningDelta            = model.EventReasoningDelta
	EventReasoningSummaryDelta     = model.EventReasoningSummaryDelta
	EventReasoningSummaryPartAdded = model.EventReasoningSummaryPartAdded
	EventReasoningContentDelta     = model.EventReasoningContentDelta
	EventToolCallInputDelta        = model.EventToolCallInputDelta
	EventOutputItemDone            = model.EventOutputItemDone
	EventServerModel               = model.EventServerModel
	EventModelVerifications        = model.EventModelVerifications
	EventTurnModerationMetadata    = model.EventTurnModerationMetadata
	EventSafetyBuffering           = model.EventSafetyBuffering
	EventServerReasoningIncluded   = model.EventServerReasoningIncluded
	EventRateLimits                = model.EventRateLimits
	EventModelsEtag                = model.EventModelsEtag
	EventCompleted                 = model.EventCompleted
)

type ResponseEvent = model.ResponseEvent
type TokenUsage = model.TokenUsage
type ClientEventType = model.ClientEventType

const (
	ClientEventTurnStarted          = model.ClientEventTurnStarted
	ClientEventTextDelta            = model.ClientEventTextDelta
	ClientEventReasoning            = model.ClientEventReasoning
	ClientEventToolCall             = model.ClientEventToolCall
	ClientEventToolResult           = model.ClientEventToolResult
	ClientEventWebSearch            = model.ClientEventWebSearch
	ClientEventHookPrompt           = model.ClientEventHookPrompt
	ClientEventImageGeneration      = model.ClientEventImageGeneration
	ClientEventPlanUpdate           = model.ClientEventPlanUpdate
	ClientEventToolApprovalRequest  = model.ClientEventToolApprovalRequest
	ClientEventToolApprovalDecision = model.ClientEventToolApprovalDecision
	ClientEventProgressNarration    = model.ClientEventProgressNarration
	ClientEventTurnCompleted        = model.ClientEventTurnCompleted
	ClientEventResponseEvent        = model.ClientEventResponseEvent
	ClientEventModelRetry           = model.ClientEventModelRetry
)

const (
	ToolTelemetryPreviewMaxBytes         = model.ToolTelemetryPreviewMaxBytes
	ToolTelemetryPreviewMaxLines         = model.ToolTelemetryPreviewMaxLines
	ToolTelemetryPreviewTruncationNotice = model.ToolTelemetryPreviewTruncationNotice
)

type ClientEvent = model.ClientEvent
type TimeReminderConfig = session.TimeReminderConfig
type TimeReminderClock = session.TimeReminderClock

var (
	UserInputItem                    = model.UserInputItem
	AssistantMessageItem             = model.AssistantMessageItem
	AssistantMessageItemFromParts    = model.AssistantMessageItemFromParts
	ToolCallItem                     = model.ToolCallItem
	ToolResultItem                   = model.ToolResultItem
	ContentPartsWithoutImageDetail   = model.ContentPartsWithoutImageDetail
	ItemsWithoutImageDetail          = model.ItemsWithoutImageDetail
	NormalizeToolResultParts         = model.NormalizeToolResultParts
	ToolResultPartsText              = model.ToolResultPartsText
	ToolResultFromContentParts       = model.ToolResultFromContentParts
	ToolResultWithoutImageInput      = model.ToolResultWithoutImageInput
	ToolResultWithoutRemoteImageURLs = model.ToolResultWithoutRemoteImageURLs
	ToolResultWithWallTime           = model.ToolResultWithWallTime
	ToolTelemetryPreview             = model.ToolTelemetryPreview
	TruncateToolResultOutput         = model.TruncateToolResultOutput
	WebSearchItem                    = model.WebSearchItem
	ImageGenerationItem              = model.ImageGenerationItem
	HookPromptItem                   = model.HookPromptItem
	ParseHookPromptParts             = model.ParseHookPromptParts
	VisibleUserInputItem             = model.VisibleUserInputItem
	IsContextualDeveloperContent     = model.IsContextualDeveloperContent
	HasNonContextualDeveloperContent = model.HasNonContextualDeveloperContent
	ReasoningItem                    = model.ReasoningItem
	ContextItem                      = model.ContextItem
	NewModelError                    = model.NewModelError
)

type Sink interface {
	OnTurnStarted(ctx context.Context, turn Turn) error
	OnTextDelta(ctx context.Context, turnID string, delta string) error
	OnReasoningDelta(ctx context.Context, turnID string, delta string) error
	OnToolCall(ctx context.Context, turnID string, call ToolCall) error
	OnToolResult(ctx context.Context, turnID string, result ToolResult) error
	OnTurnCompleted(ctx context.Context, turn Turn) error
}

type NopSink struct{}

func (NopSink) OnTurnStarted(context.Context, Turn) error {
	return nil
}

func (NopSink) OnTextDelta(context.Context, string, string) error {
	return nil
}

func (NopSink) OnReasoningDelta(context.Context, string, string) error {
	return nil
}

func (NopSink) OnToolCall(context.Context, string, ToolCall) error {
	return nil
}

func (NopSink) OnToolResult(context.Context, string, ToolResult) error {
	return nil
}

func (NopSink) OnTurnCompleted(context.Context, Turn) error {
	return nil
}

type ResponseEventSink interface {
	OnResponseEvent(ctx context.Context, turnID string, event ResponseEvent) error
}

type ClientEventSink interface {
	OnClientEvent(ctx context.Context, event ClientEvent) error
}

type EventStream interface {
	Recv() (ResponseEvent, error)
}

type ModelClient interface {
	Stream(ctx context.Context, prompt Prompt) (EventStream, error)
}

type TurnResult = runner.TurnResult
type TurnMetrics = model.TurnMetrics
type TurnProfile = model.TurnProfile

// Hooks are Codex loop extension points exposed as library callbacks. They are
// intentionally ordered like Codex: prompt hooks before sampling, call mutation
// before guardrail review, guardrail hook before reviewer, and result hook after
// dispatch. ToolLifecycle is observational and preserves Codex's start/finish
// outcome metadata for embedders that need audit traces.
type Hooks struct {
	BeforeModelRequest func(context.Context, Turn, Prompt) (Prompt, error)
	AfterModelRequest  func(context.Context, Turn, Prompt, error) error
	BeforeToolCall     func(context.Context, Turn, ToolCall) (ToolCall, error)
	AfterToolCall      func(context.Context, Turn, ToolCall, Item) (Item, error)
	// ToolLifecycle may be called concurrently for independent in-flight tool
	// calls; protect shared observer state in the callback.
	ToolLifecycle  func(context.Context, Turn, ToolLifecycleEvent) error
	ReviewToolCall func(context.Context, Turn, ToolApprovalRequest) (ApprovalDecision, error)
}

type ToolApprovalReviewer func(context.Context, Turn, ToolApprovalRequest) (ApprovalDecision, error)

// Guardrails let callers opt into Codex-style approval gates without forcing
// Dexco to own a specific OS sandbox or UI approval surface.
type Guardrails struct {
	ApprovalPolicy ApprovalPolicy
	Reviewer       ToolApprovalReviewer
	// PermissionGrants adapts Codex request_permissions grants for library
	// callers. A guarded tool can set ToolGuardrail.PermissionGrantKey; if this
	// store has a matching turn/session grant, Dexco skips repeated approval.
	PermissionGrants *PermissionGrantStore
}

// RunnerOptions collects optional Codex behaviors that are not required for the
// minimal LLM loop: stream retries, hooks, parallel-safe tool execution, and
// approval guardrails.
type RunnerOptions struct {
	MaxModelRetries int
	// ToolResultMaxChars bounds text tool output before it enters
	// model-visible history. This mirrors Codex's context-manager truncation for
	// function/custom-tool outputs. Zero uses Dexco's default; negative disables
	// truncation for embedders that already applied a hard cap.
	ToolResultMaxChars int
	RetryBackoff       func(attempt int, err error) time.Duration
	Hooks              Hooks
	ParallelTools      bool
	Guardrails         Guardrails
	// Clock is optional and primarily intended for deterministic telemetry
	// tests. Nil uses time.Now.
	Clock             func() time.Time
	progressNarration ProgressNarrationConfig
}

type Runner struct {
	inner *runner.Runner
}

func NewRunner(modelClient ModelClient, router *Router) (*Runner, error) {
	return NewRunnerWithOptions(modelClient, router, RunnerOptions{})
}

func NewRunnerWithOptions(
	modelClient ModelClient,
	router *Router,
	options RunnerOptions,
) (*Runner, error) {
	if modelClient == nil {
		return nil, fmt.Errorf("new runner: nil model client")
	}
	if router == nil {
		return nil, fmt.Errorf("new runner: nil router")
	}
	inner, err := runner.NewWithOptions(
		modelClientAdapter{client: modelClient},
		router.inner,
		internalRunnerOptions(options),
	)
	if err != nil {
		return nil, err
	}
	return &Runner{inner: inner}, nil
}

func (r *Runner) RunTurn(ctx context.Context, turn Turn, sink Sink) (TurnResult, error) {
	if r == nil || r.inner == nil {
		return TurnResult{}, fmt.Errorf("run turn: nil runner")
	}
	if sink == nil {
		return r.inner.RunTurn(ctx, turn, nil)
	}
	return r.inner.RunTurn(ctx, turn, sinkAdapter{sink: sink})
}

type Config = session.Config
type ModelSwitchInstructionsConfig = session.ModelSwitchInstructionsConfig
type PermissionInstructionsConfig = session.PermissionInstructionsConfig
type CollaborationInstructionsConfig = session.CollaborationInstructionsConfig
type StyleInstructionsConfig = session.StyleInstructionsConfig
type UserShellCommandRecord = session.UserShellCommandRecord
type SteerUserInputOptions = session.SteerUserInputOptions
type SubagentNotificationRecord = session.SubagentNotificationRecord
type ContextInstructionsConfig = session.ContextInstructionsConfig
type ContextInstructionsSnapshot = session.ContextInstructionsSnapshot
type InstructionSource = session.InstructionSource
type EnvironmentContextConfig = session.EnvironmentContextConfig
type EnvironmentContextSnapshot = session.EnvironmentContextSnapshot
type EnvironmentState = session.EnvironmentState
type RolloutBudgetConfig = session.RolloutBudgetConfig

var ErrRolloutBudgetExceeded = session.ErrRolloutBudgetExceeded

type Session struct {
	inner *session.Session
}

func NewSession(cfg Config, turnRunner *Runner) (*Session, error) {
	if turnRunner == nil {
		return nil, fmt.Errorf("new session: nil runner")
	}
	inner, err := session.New(cfg, turnRunner.inner)
	if err != nil {
		return nil, err
	}
	return &Session{inner: inner}, nil
}

func (s *Session) SubmitUserInput(
	ctx context.Context,
	op OpUserInput,
	sink Sink,
) (TurnResult, error) {
	if s == nil || s.inner == nil {
		return TurnResult{}, fmt.Errorf("submit user input: nil session")
	}
	if sink == nil {
		return s.inner.SubmitUserInput(ctx, op, nil)
	}
	return s.inner.SubmitUserInput(ctx, op, sinkAdapter{sink: sink})
}

func (s *Session) SteerUserInput(ctx context.Context, input UserInput) error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("steer user input: nil session")
	}
	return s.inner.SteerUserInput(ctx, input)
}

func (s *Session) SteerUserInputWithOptions(
	ctx context.Context,
	input UserInput,
	options SteerUserInputOptions,
) (string, error) {
	if s == nil || s.inner == nil {
		return "", fmt.Errorf("steer user input: nil session")
	}
	return s.inner.SteerUserInputWithOptions(ctx, input, options)
}

func (s *Session) SetPermissionInstructions(text string) error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("set permission instructions: nil session")
	}
	s.inner.SetPermissionInstructions(text)
	return nil
}

func (s *Session) SetModelSwitchInstructions(text string) error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("set model switch instructions: nil session")
	}
	s.inner.SetModelSwitchInstructions(text)
	return nil
}

func (s *Session) SetCollaborationInstructions(text string) error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("set collaboration instructions: nil session")
	}
	s.inner.SetCollaborationInstructions(text)
	return nil
}

func (s *Session) SetStyleInstructions(text string) error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("set style instructions: nil session")
	}
	s.inner.SetStyleInstructions(text)
	return nil
}

func (s *Session) RecordUserShellCommand(record UserShellCommandRecord) error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("record user shell command: nil session")
	}
	return s.inner.RecordUserShellCommand(record)
}

func (s *Session) RecordSubagentNotification(record SubagentNotificationRecord) error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("record subagent notification: nil session")
	}
	return s.inner.RecordSubagentNotification(record)
}

func (s *Session) SetContextInstructions(snapshot ContextInstructionsSnapshot) error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("set context instructions: nil session")
	}
	return s.inner.SetContextInstructions(snapshot)
}

func (s *Session) ClearContextInstructions() error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("clear context instructions: nil session")
	}
	return s.inner.ClearContextInstructions()
}

func (s *Session) ContextInstructionSources() []InstructionSource {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.ContextInstructionSources()
}

func (s *Session) SetEnvironmentContext(snapshot EnvironmentContextSnapshot) error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("set environment context: nil session")
	}
	return s.inner.SetEnvironmentContext(snapshot)
}

func (s *Session) ClearEnvironmentContext() error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("clear environment context: nil session")
	}
	return s.inner.ClearEnvironmentContext()
}

type Handler interface {
	Name() string
	Spec() ToolSpec
	Call(ctx context.Context, call ToolCall) (ToolResult, error)
}

// GuardedHandler is implemented by tools that can classify their own risk
// before execution. This is Dexco's public seam for adopting future Codex policy
// improvements without changing the Handler call contract.
type GuardedHandler interface {
	Guardrail(ctx context.Context, call ToolCall) (ToolGuardrail, error)
}

// ParallelHandler opts a handler into concurrent execution when the model emits
// adjacent calls. Without this marker, Dexco preserves Codex's conservative
// serial execution behavior.
type ParallelHandler interface {
	SupportsParallel() bool
}

// PendingInputInterruptHandler opts a handler into Codex-style pending-input
// wakeups. Dexco cancels the tool call context when new input is steered into
// the active turn; the handler should return a normal ToolResult describing the
// interruption rather than treating that cancellation as a hard turn failure.
type PendingInputInterruptHandler interface {
	InterruptsOnPendingInput() bool
}

type Router struct {
	inner *tools.Router
}

func NewRouter(handlers ...Handler) (*Router, error) {
	inner, err := tools.NewRouter()
	if err != nil {
		return nil, err
	}
	router := &Router{inner: inner}
	for _, handler := range handlers {
		if err := router.Register(handler); err != nil {
			return nil, err
		}
	}
	return router, nil
}

func (r *Router) Register(handler Handler) error {
	if r == nil || r.inner == nil {
		return fmt.Errorf("register handler: nil router")
	}
	return r.inner.Register(handlerAdapter{handler: handler})
}

// RegisterDeferred registers a Codex-style deferred tool. Deferred tools are
// searchable through Dexco's synthetic `tool_search` tool and callable by exact
// name, but they are not advertised in Prompt.Tools until the model discovers
// them. searchText should contain extra metadata embedders want indexed beyond
// the handler's name, description, and parameters.
func (r *Router) RegisterDeferred(handler Handler, searchText string) error {
	if r == nil || r.inner == nil {
		return fmt.Errorf("register deferred handler: nil router")
	}
	return r.inner.RegisterDeferred(handlerAdapter{handler: handler}, searchText)
}

func (r *Router) Specs() []ToolSpec {
	if r == nil || r.inner == nil {
		return nil
	}
	return r.inner.Specs()
}

func (r *Router) Dispatch(ctx context.Context, call ToolCall) (Item, error) {
	if r == nil || r.inner == nil {
		return Item{}, fmt.Errorf("dispatch tool %q: nil router", call.Name)
	}
	return r.inner.Dispatch(ctx, call)
}

func (r *Router) Guardrail(ctx context.Context, call ToolCall) (ToolGuardrail, error) {
	if r == nil || r.inner == nil {
		return ToolGuardrail{}, fmt.Errorf("tool guardrail %q: nil router", call.Name)
	}
	return r.inner.Guardrail(ctx, call)
}

func (r *Router) SupportsParallel(call ToolCall) bool {
	if r == nil || r.inner == nil {
		return false
	}
	return r.inner.SupportsParallel(call)
}

func (r *Router) InterruptsOnPendingInput(call ToolCall) bool {
	if r == nil || r.inner == nil {
		return false
	}
	return r.inner.InterruptsOnPendingInput(call)
}

type ExecCommandHandler = builtin.ExecCommandHandler
type ExecCommandArgs = builtin.ExecCommandArgs
type SleepHandler = builtin.SleepHandler
type SleepArgs = builtin.SleepArgs
type CurrentTimeHandler = builtin.CurrentTimeHandler
type RequestUserInputHandler = builtin.RequestUserInputHandler
type RequestPermissionsHandler = builtin.RequestPermissionsHandler
type UpdatePlanHandler = builtin.UpdatePlanHandler
type ViewImageHandler = builtin.ViewImageHandler
type ViewImageArgs = builtin.ViewImageArgs
type RequestUserInputOption = builtin.RequestUserInputOption
type RequestUserInputQuestion = builtin.RequestUserInputQuestion
type RequestUserInputArgs = builtin.RequestUserInputArgs
type RequestUserInputAnswer = builtin.RequestUserInputAnswer
type RequestUserInputResponse = builtin.RequestUserInputResponse
type PermissionGrantResponder = builtin.PermissionGrantResponder
type RequestPermissionsArgs = builtin.RequestPermissionsArgs
type RequestPermissionsResponse = builtin.RequestPermissionsResponse
type UserInputResponder = builtin.UserInputResponder
type StructuredUserInputResponder = builtin.StructuredUserInputResponder

const MaxSleepDurationMS = builtin.MaxSleepDurationMS

func CodingWorkflowHandlers(responder UserInputResponder) []Handler {
	internalHandlers := builtin.CodingWorkflowHandlers(responder)
	handlers := make([]Handler, 0, len(internalHandlers))
	for _, handler := range internalHandlers {
		handlers = append(handlers, handler)
	}
	return handlers
}

func NewCodingWorkflowRouter(responder UserInputResponder) (*Router, error) {
	return NewRouter(CodingWorkflowHandlers(responder)...)
}

func NewCodingWorkflowSession(
	cfg Config,
	modelClient ModelClient,
	responder UserInputResponder,
) (*Session, error) {
	return NewCodingWorkflowSessionWithOptions(cfg, modelClient, responder, RunnerOptions{})
}

func NewCodingWorkflowSessionWithOptions(
	cfg Config,
	modelClient ModelClient,
	responder UserInputResponder,
	options RunnerOptions,
) (*Session, error) {
	router, err := NewCodingWorkflowRouter(responder)
	if err != nil {
		return nil, err
	}
	turnRunner, err := NewRunnerWithOptions(modelClient, router, options)
	if err != nil {
		return nil, err
	}
	return NewSession(cfg, turnRunner)
}

func internalRunnerOptions(options RunnerOptions) runner.Options {
	var reviewer runner.ToolApprovalReviewer
	if options.Guardrails.Reviewer != nil {
		reviewer = runner.ToolApprovalReviewer(options.Guardrails.Reviewer)
	}
	return runner.Options{
		MaxModelRetries:    options.MaxModelRetries,
		ToolResultMaxChars: options.ToolResultMaxChars,
		RetryBackoff:       options.RetryBackoff,
		Hooks: runner.Hooks{
			BeforeModelRequest: options.Hooks.BeforeModelRequest,
			AfterModelRequest:  options.Hooks.AfterModelRequest,
			BeforeToolCall:     options.Hooks.BeforeToolCall,
			AfterToolCall:      options.Hooks.AfterToolCall,
			ToolLifecycle:      options.Hooks.ToolLifecycle,
			ReviewToolCall:     options.Hooks.ReviewToolCall,
		},
		ParallelTools:     options.ParallelTools,
		Clock:             options.Clock,
		ProgressNarration: model.ProgressNarrationConfig(options.progressNarration),
		Guardrails: runner.Guardrails{
			ApprovalPolicy:   options.Guardrails.ApprovalPolicy,
			Reviewer:         reviewer,
			PermissionGrants: options.Guardrails.PermissionGrants,
		},
	}
}

type modelClientAdapter struct {
	client ModelClient
}

func (a modelClientAdapter) Stream(ctx context.Context, prompt model.Prompt) (runner.EventStream, error) {
	return a.client.Stream(ctx, prompt)
}

type handlerAdapter struct {
	handler Handler
}

func (a handlerAdapter) Name() string {
	if a.handler == nil {
		return ""
	}
	return a.handler.Name()
}

func (a handlerAdapter) Spec() model.ToolSpec {
	if a.handler == nil {
		return model.ToolSpec{}
	}
	return a.handler.Spec()
}

func (a handlerAdapter) Call(ctx context.Context, call model.ToolCall) (model.ToolResult, error) {
	if a.handler == nil {
		return model.ToolResult{}, fmt.Errorf("nil handler")
	}
	return a.handler.Call(ctx, call)
}

func (a handlerAdapter) Guardrail(ctx context.Context, call model.ToolCall) (model.ToolGuardrail, error) {
	guardedHandler, ok := a.handler.(GuardedHandler)
	if !ok {
		return model.ToolGuardrail{
			Risk:                model.ToolRiskUnknown,
			ApprovalRequirement: model.ApprovalRequirementNone,
		}, nil
	}
	return guardedHandler.Guardrail(ctx, call)
}

func (a handlerAdapter) SupportsParallel() bool {
	parallelHandler, ok := a.handler.(ParallelHandler)
	return ok && parallelHandler.SupportsParallel()
}

func (a handlerAdapter) InterruptsOnPendingInput() bool {
	interruptHandler, ok := a.handler.(PendingInputInterruptHandler)
	return ok && interruptHandler.InterruptsOnPendingInput()
}

type sinkAdapter struct {
	sink Sink
}

func (a sinkAdapter) OnTurnStarted(ctx context.Context, turn model.Turn) error {
	return a.sink.OnTurnStarted(ctx, turn)
}

func (a sinkAdapter) OnTextDelta(ctx context.Context, turnID string, delta string) error {
	return a.sink.OnTextDelta(ctx, turnID, delta)
}

func (a sinkAdapter) OnReasoningDelta(ctx context.Context, turnID string, delta string) error {
	return a.sink.OnReasoningDelta(ctx, turnID, delta)
}

func (a sinkAdapter) OnToolCall(ctx context.Context, turnID string, call model.ToolCall) error {
	return a.sink.OnToolCall(ctx, turnID, call)
}

func (a sinkAdapter) OnToolResult(ctx context.Context, turnID string, result model.ToolResult) error {
	return a.sink.OnToolResult(ctx, turnID, result)
}

func (a sinkAdapter) OnTurnCompleted(ctx context.Context, turn model.Turn) error {
	return a.sink.OnTurnCompleted(ctx, turn)
}

func (a sinkAdapter) OnResponseEvent(
	ctx context.Context,
	turnID string,
	event model.ResponseEvent,
) error {
	rawSink, ok := a.sink.(ResponseEventSink)
	if !ok {
		return nil
	}
	return rawSink.OnResponseEvent(ctx, turnID, event)
}

func (a sinkAdapter) OnClientEvent(ctx context.Context, event model.ClientEvent) error {
	clientSink, ok := a.sink.(ClientEventSink)
	if !ok {
		return nil
	}
	return clientSink.OnClientEvent(ctx, event)
}
