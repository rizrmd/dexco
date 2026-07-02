package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/rizrmd/dexco/internal/events"
	"github.com/rizrmd/dexco/internal/model"
	permissionstore "github.com/rizrmd/dexco/internal/permissions"
	"github.com/rizrmd/dexco/internal/tools"
)

type EventStream interface {
	Recv() (model.ResponseEvent, error)
}

type ModelClient interface {
	Stream(ctx context.Context, prompt model.Prompt) (EventStream, error)
}

type TurnResult struct {
	TurnID       string
	History      []model.Item
	FinalMessage string
	ModelCalls   int
	Status       model.TurnStatus
	Metrics      model.TurnMetrics
	TokenUsages  []model.TokenUsage
}

type samplingAttemptResult struct {
	items         []model.Item
	toolResults   []toolDispatchResult
	tokenUsages   []model.TokenUsage
	finalMessage  string
	needsFollowUp bool
	turnState     string
}

type toolDispatchResult struct {
	item         model.Item
	clientEvents []model.ClientEvent
}

type inFlightToolDispatcher struct {
	runner               *Runner
	turn                 model.Turn
	sink                 events.Sink
	pendingInputActivity PendingInputActivityFunc
	guardrailDenials     *guardrailDenialCircuitBreaker
	progress             *progressNarrator
	lock                 sync.RWMutex
	pending              []inFlightToolCall
}

type inFlightToolCall struct {
	call   model.ToolCall
	result <-chan inFlightToolResult
}

type inFlightToolResult struct {
	result toolDispatchResult
	err    error
}

type streamReceiveError struct {
	err error
}

type TurnAbortedError struct {
	err     error
	history []model.Item
}

const (
	guardrailMaxConsecutiveDenialsPerTurn = 3
	guardrailMaxRecentDenialsPerTurn      = 10
	guardrailDenialWindowSize             = 50
)

type guardrailDenialCircuitBreaker struct {
	mu                 sync.Mutex
	consecutiveDenials int
	recentDenials      []bool
	interruptTriggered bool
}

func (b *guardrailDenialCircuitBreaker) recordDenial(turnID string) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	b.consecutiveDenials++
	b.recordRecentReview(true)
	recentDenials := 0
	for _, denied := range b.recentDenials {
		if denied {
			recentDenials++
		}
	}
	if !b.interruptTriggered &&
		(b.consecutiveDenials >= guardrailMaxConsecutiveDenialsPerTurn ||
			recentDenials >= guardrailMaxRecentDenialsPerTurn) {
		b.interruptTriggered = true
		// Dexco keeps Codex's guardrail-rejection circuit breaker in the core
		// loop, not in a UI layer. A model that repeatedly asks for denied
		// actions should not be allowed to spin forever across follow-up model
		// requests. Codex emits a GuardianWarning and aborts the active turn;
		// Dexco has no Guardian event channel, so it returns a hard turn error
		// with the same thresholds and counts.
		return fmt.Errorf(
			"guardrail denied too many tool calls for turn %q (%d consecutive, %d in the last %d reviews); interrupting the turn",
			turnID,
			b.consecutiveDenials,
			recentDenials,
			guardrailDenialWindowSize,
		)
	}
	return nil
}

func (b *guardrailDenialCircuitBreaker) recordNonDenial() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	// This mirrors Codex's record_non_denial behavior: an allowed or approved
	// action breaks the consecutive-denial chain, while the bounded recent
	// review window still remembers earlier denials for loop protection.
	b.consecutiveDenials = 0
	b.recordRecentReview(false)
}

func (b *guardrailDenialCircuitBreaker) recordRecentReview(denied bool) {
	b.recentDenials = append(b.recentDenials, denied)
	if len(b.recentDenials) > guardrailDenialWindowSize {
		copy(b.recentDenials, b.recentDenials[1:])
		b.recentDenials = b.recentDenials[:guardrailDenialWindowSize]
	}
}

// newTurnAbortedError is Dexco's compact equivalent of Codex's interrupted-turn
// history bookkeeping. Codex records the completed tool call, a synthetic
// aborted tool output, and a <turn_aborted> context marker so the next model
// request knows the previous side effect may have partially executed. Dexco
// keeps that transcript on the error instead of committing a completed turn.
func newTurnAbortedError(err error, history []model.Item) *TurnAbortedError {
	return &TurnAbortedError{
		err:     err,
		history: appendCommittedItems(history, []model.Item{model.TurnAbortedItem()}),
	}
}

func (e *TurnAbortedError) Error() string {
	if e.err == nil {
		return "turn aborted"
	}
	return e.err.Error()
}

func (e *TurnAbortedError) Unwrap() error {
	return e.err
}

// AbortedHistory exposes the model-visible transcript that should survive an
// interrupted turn. Session owns durable history, so the runner returns this
// out-of-band rather than mutating history after a turn failed.
func AbortedHistory(err error) ([]model.Item, bool) {
	var aborted *TurnAbortedError
	if !errors.As(err, &aborted) {
		return nil, false
	}
	return appendCommittedItems(nil, aborted.history), true
}

func (e streamReceiveError) Error() string {
	return e.err.Error()
}

func (e streamReceiveError) Unwrap() error {
	return e.err
}

type Hooks struct {
	BeforeModelRequest func(context.Context, model.Turn, model.Prompt) (model.Prompt, error)
	AfterModelRequest  func(context.Context, model.Turn, model.Prompt, error) error
	BeforeToolCall     func(context.Context, model.Turn, model.ToolCall) (model.ToolCall, error)
	AfterToolCall      func(context.Context, model.Turn, model.ToolCall, model.Item) (model.Item, error)
	// ToolLifecycle mirrors Codex's ToolLifecycleContributor callbacks at the
	// library boundary. It is observational: BeforeToolCall/AfterToolCall remain
	// the mutation hooks, while this hook receives start/finish outcome metadata
	// that distinguishes handler errors from ordinary failed tool results. It may
	// be called concurrently for independent in-flight tool calls, matching Codex
	// early tool-dispatch behavior.
	ToolLifecycle func(context.Context, model.Turn, model.ToolLifecycleEvent) error
	// ReviewToolCall corresponds to Codex permission request hooks. It runs
	// before the reviewer so local policy can allow/deny deterministically, or
	// return NoDecision to fall through to the normal approval path.
	ReviewToolCall func(context.Context, model.Turn, model.ToolApprovalRequest) (model.ApprovalDecision, error)
}

type ToolApprovalReviewer func(context.Context, model.Turn, model.ToolApprovalRequest) (model.ApprovalDecision, error)

type Guardrails struct {
	// ApprovalPolicy is Dexco's compact analogue of Codex's approval policy.
	// Dexco does not own sandbox backends, so the policy gates tool dispatch
	// before side effects instead of retrying with a different sandbox.
	ApprovalPolicy model.ApprovalPolicy
	// Reviewer models Codex's user/guardian reviewer step. Permission hooks
	// above have precedence; this callback is only reached when hooks return
	// NoDecision and the selected policy still requires approval.
	Reviewer ToolApprovalReviewer
	// PermissionGrants is Dexco's library-level adaptation of Codex's
	// request_permissions state. Tools provide opaque grant keys in their
	// guardrail; matching turn/session grants skip repeated approval without
	// requiring Dexco to own filesystem or network sandbox policy.
	PermissionGrants *permissionstore.Store
}

type Options struct {
	MaxModelRetries int
	// ToolResultMaxChars bounds text tool output before it is recorded in
	// model-visible history. Codex applies this in its ContextManager so failed
	// or verbose tools cannot consume future prompt budget. Zero uses Dexco's
	// conservative default; negative disables truncation for embedders that
	// already enforce a hard cap.
	ToolResultMaxChars int
	RetryBackoff       func(attempt int, err error) time.Duration
	Hooks              Hooks
	// ParallelTools mirrors Codex's batch behavior: consecutive tool calls that
	// are explicitly marked parallel-safe may run concurrently, but their results
	// are replayed to history in model order.
	ParallelTools     bool
	Guardrails        Guardrails
	ProgressNarration model.ProgressNarrationConfig
	HistoryProtection model.HistoryProtectionConfig
	// Clock exists so Codex timing parity tests can be deterministic. Production
	// callers should leave it nil and use time.Now.
	Clock func() time.Time
}

type PendingInputFunc func(context.Context) ([]model.Item, error)
type PendingInputActivityFunc func(context.Context) <-chan struct{}

type TurnOptions struct {
	// PendingInput is Dexco's compact session-level adaptation of Codex pending
	// input. Codex can receive new user/mail input while a turn is active; the
	// runner must append it only at a safe continuation boundary, never into a
	// failed retry attempt or the middle of an in-flight response item.
	PendingInput PendingInputFunc
	// PendingInputActivity returns a per-turn notification channel that closes
	// when new pending input is queued. Interruptible wait/sleep-style tools use
	// this to wake early, mirroring Codex's input_queue activity subscription.
	PendingInputActivity PendingInputActivityFunc
}

type Runner struct {
	modelClient ModelClient
	router      *tools.Router
	options     Options
}

const defaultToolResultMaxChars = 20_000

func New(modelClient ModelClient, router *tools.Router) (*Runner, error) {
	return NewWithOptions(modelClient, router, Options{})
}

func NewWithOptions(modelClient ModelClient, router *tools.Router, options Options) (*Runner, error) {
	if modelClient == nil {
		return nil, fmt.Errorf("new runner: nil model client")
	}
	if router == nil {
		return nil, fmt.Errorf("new runner: nil router")
	}

	return &Runner{
		modelClient: modelClient,
		router:      router,
		options:     options,
	}, nil
}

func (r *Runner) RunTurn(ctx context.Context, turn model.Turn, sink events.Sink) (TurnResult, error) {
	return r.RunTurnWithOptions(ctx, turn, sink, TurnOptions{})
}

func (r *Runner) RunTurnWithOptions(
	ctx context.Context,
	turn model.Turn,
	sink events.Sink,
	turnOptions TurnOptions,
) (TurnResult, error) {
	if sink == nil {
		sink = events.NopSink{}
	}
	if r.options.Guardrails.PermissionGrants != nil {
		defer r.options.Guardrails.PermissionGrants.ClearTurn(turn.ID)
	}

	history := append([]model.Item(nil), turn.History...)
	protectedInitialHistory, historyProtectionDeveloperMessages := model.ProtectPromptHistory(turn.History, r.options.HistoryProtection)
	initialHistoryLen := len(turn.History)
	turnState := ""
	metrics := newTurnMetricsState(r.now)
	guardrailDenials := &guardrailDenialCircuitBreaker{}
	result := TurnResult{
		TurnID: turn.ID,
		Status: model.TurnStatusRunning,
	}

	for {
		// Codex loop parity: each sampling request is built from the committed
		// conversation history plus current instructions and tool specs. Failed
		// stream attempts never mutate this history, preserving prompt-cache and
		// retry semantics.
		promptHistory := append([]model.Item(nil), protectedInitialHistory...)
		promptHistory = append(promptHistory, history[initialHistoryLen:]...)
		promptDeveloperMessages := append([]string(nil), turn.DeveloperMessages...)
		promptDeveloperMessages = append(promptDeveloperMessages, historyProtectionDeveloperMessages...)
		prompt := model.Prompt{
			History:           promptHistory,
			Instructions:      turn.Instructions,
			DeveloperMessages: promptDeveloperMessages,
			Tools:             r.router.Specs(),
			TurnState:         turnState,
			WebSearch:         model.CloneWebSearchRequest(turn.WebSearch),
			OutputSchema:      append([]byte(nil), turn.OutputSchema...),
		}
		if r.options.Hooks.BeforeModelRequest != nil {
			var err error
			prompt, err = r.options.Hooks.BeforeModelRequest(ctx, turn, prompt)
			if err != nil {
				return TurnResult{}, fmt.Errorf("before model request hook: %w", err)
			}
		}

		attempt, attempts, err := r.runSamplingRequest(
			ctx,
			turn,
			prompt,
			sink,
			turnOptions,
			metrics,
			guardrailDenials,
		)
		result.ModelCalls += attempts
		if err != nil {
			return TurnResult{}, err
		}
		result.TokenUsages = append(result.TokenUsages, attempt.tokenUsages...)
		history = append(history, attempt.items...)
		result.FinalMessage = attempt.finalMessage
		if turnState == "" && attempt.turnState != "" {
			// Codex creates a fresh ModelClientSession per turn and stores
			// `x-codex-turn-state` in an OnceLock. That means the first
			// provider value is replayed for all later same-turn requests, later
			// provider values are ignored, and the token never becomes durable
			// history. Dexco keeps the same contract as Prompt metadata so
			// provider adapters can encode it as headers or websocket metadata.
			turnState = attempt.turnState
		}

		finalResponse := ""
		for _, toolResult := range attempt.toolResults {
			for _, event := range toolResult.clientEvents {
				if err := emitClientEvent(ctx, sink, event); err != nil {
					return TurnResult{}, fmt.Errorf("emit tool guardrail client event: %w", err)
				}
			}
			if toolResult.item.ToolResult != nil {
				if toolResult.item.ToolResult.PlanUpdate != nil {
					// Codex emits PlanUpdate as a client-facing event and a
					// separate "Plan updated" tool output to the model. Dexco
					// keeps the metadata on ToolResult until this point so hooks,
					// guardrails, and parallel dispatch all use the ordinary tool
					// path before clients receive the richer event, followed by
					// the ordinary model-visible tool result.
					if err := emitClientEvent(ctx, sink, model.ClientEvent{
						Type:       model.ClientEventPlanUpdate,
						TurnID:     turn.ID,
						PlanUpdate: toolResult.item.ToolResult.PlanUpdate,
					}); err != nil {
						return TurnResult{}, fmt.Errorf("emit plan update client event: %w", err)
					}
				}
				modelVisibleResult := r.modelVisibleToolResult(*toolResult.item.ToolResult)
				modelVisibleResult.PlanUpdate = nil
				history = append(history, model.ToolResultItem(modelVisibleResult))
				if err := sink.OnToolResult(ctx, turn.ID, modelVisibleResult); err != nil {
					return TurnResult{}, fmt.Errorf("emit tool result: %w", err)
				}
				if err := emitClientEvent(ctx, sink, model.ClientEvent{
					Type:       model.ClientEventToolResult,
					TurnID:     turn.ID,
					ToolResult: &modelVisibleResult,
				}); err != nil {
					return TurnResult{}, fmt.Errorf("emit tool result client event: %w", err)
				}
				if finalResponse == "" && modelVisibleResult.Success {
					if response := strings.TrimSpace(modelVisibleResult.FinalResponse); response != "" {
						finalResponse = response
					}
				}
			} else {
				history = append(history, toolResult.item)
			}
		}

		pendingItems, err := drainPendingInput(ctx, turnOptions)
		if err != nil {
			return TurnResult{}, err
		}
		if len(pendingItems) > 0 {
			// Codex pending_input parity: new input observed during an active
			// turn is replayed as model-visible history at the next safe
			// continuation point. This keeps in-flight model output durable while
			// ensuring the follow-up request sees the user's latest steering.
			history = append(history, pendingItems...)
			attempt.needsFollowUp = true
		}
		if finalResponse != "" && len(pendingItems) == 0 {
			result.FinalMessage = finalResponse
			history = append(history, model.AssistantMessageItem(finalResponse))
			break
		}

		// Codex loop parity: a model turn can complete a sampling request but
		// still require a follow-up request because tools ran or EndTurn=false.
		if !attempt.needsFollowUp {
			break
		}

	}

	// Codex loop parity: only after all follow-up sampling requests finish do we
	// publish the completed turn and commit the accumulated history.
	result.History = history
	result.Status = model.TurnStatusCompleted
	result.Metrics = metrics.Complete()
	completedTurn := model.Turn{
		ID:                turn.ID,
		History:           history,
		Instructions:      turn.Instructions,
		DeveloperMessages: append([]string(nil), turn.DeveloperMessages...),
		OutputSchema:      append([]byte(nil), turn.OutputSchema...),
		Status:            model.TurnStatusCompleted,
	}
	if err := sink.OnTurnCompleted(ctx, completedTurn); err != nil {
		return TurnResult{}, fmt.Errorf("turn completed event: %w", err)
	}
	if err := emitClientEvent(ctx, sink, model.ClientEvent{
		Type:   model.ClientEventTurnCompleted,
		TurnID: turn.ID,
		Turn:   &completedTurn,
	}); err != nil {
		return TurnResult{}, fmt.Errorf("turn completed client event: %w", err)
	}
	return result, nil
}

func (r *Runner) modelVisibleToolResult(result model.ToolResult) model.ToolResult {
	result = cloneToolResult(result)
	maxChars := r.options.ToolResultMaxChars
	if maxChars == 0 {
		maxChars = defaultToolResultMaxChars
	}
	return model.TruncateToolResultOutput(result, maxChars)
}

func drainPendingInput(ctx context.Context, options TurnOptions) ([]model.Item, error) {
	if options.PendingInput == nil {
		return nil, nil
	}
	items, err := options.PendingInput(ctx)
	if err != nil {
		return nil, fmt.Errorf("drain pending input: %w", err)
	}
	return append([]model.Item(nil), items...), nil
}

func (r *Runner) runSamplingRequest(
	ctx context.Context,
	turn model.Turn,
	prompt model.Prompt,
	sink events.Sink,
	turnOptions TurnOptions,
	metrics *turnMetricsState,
	guardrailDenials *guardrailDenialCircuitBreaker,
) (samplingAttemptResult, int, error) {
	attempts := 0
	baseHistory := append([]model.Item(nil), prompt.History...)
	committedRetryItems := make([]model.Item, 0)
	for {
		metrics.BeginSampling()
		modelStartProgress := newProgressNarrator(
			r.options.ProgressNarration,
			sink,
			turn.ID,
			r.now,
		)
		stopModelStartProgress := func() {}
		if modelStartProgress != nil {
			stopModelStartProgress = modelStartProgress.StartWork(
				ctx,
				model.WorkPhaseWaitingForModel,
				model.ProgressHint{Label: "Waiting for model"},
				"",
			)
		}
		stream, err := r.modelClient.Stream(ctx, prompt)
		stopModelStartProgress()
		if modelStartProgress != nil {
			modelStartProgress.Close()
		}
		attempts++
		if err != nil {
			metrics.EndSampling()
		}
		if r.options.Hooks.AfterModelRequest != nil {
			if hookErr := r.options.Hooks.AfterModelRequest(ctx, turn, prompt, err); hookErr != nil {
				metrics.EndSampling()
				return samplingAttemptResult{}, attempts, fmt.Errorf("after model request hook: %w", hookErr)
			}
		}
		if err == nil {
			attemptSink := sink
			var buffered *bufferedSink
			if r.options.MaxModelRetries > 0 {
				// Codex retries incomplete/disconnected streams without making
				// partial deltas or tool calls visible. Buffer sink callbacks
				// until response.completed proves the attempt is durable.
				buffered = newBufferedSink(sink)
				attemptSink = buffered
			}
			attempt, receiveErr := r.receiveSamplingAttempt(
				ctx,
				turn,
				stream,
				attemptSink,
				turnOptions,
				metrics,
				guardrailDenials,
			)
			if receiveErr == nil {
				if buffered != nil {
					if flushErr := buffered.Flush(ctx); flushErr != nil {
						return samplingAttemptResult{}, attempts, flushErr
					}
				}
				if len(committedRetryItems) > 0 {
					attempt.items = appendCommittedItems(committedRetryItems, attempt.items)
				}
				return attempt, attempts, nil
			}
			if !isStreamReceiveError(receiveErr) {
				if isTurnCancellation(receiveErr) {
					committed := materializeAttemptItems(attempt)
					if len(committed) > 0 {
						return samplingAttemptResult{}, attempts, newTurnAbortedError(receiveErr, committed)
					}
				}
				return samplingAttemptResult{}, attempts, receiveErr
			}
			committed := materializeAttemptItems(attempt)
			if len(committed) > 0 {
				// Codex records completed response items and drains in-flight
				// tool outputs even if the stream fails before response.completed.
				// That transcript is then used as the next retry prompt. Dexco
				// keeps UI events buffered until a durable completed response, but
				// the model-visible history must still include side effects that
				// already ran so retries do not duplicate or forget them.
				committedRetryItems = append(committedRetryItems, committed...)
				prompt.History = appendCommittedItems(baseHistory, committedRetryItems)
			}
			err = receiveErr
		}
		if isNonRetryableModelError(err) {
			return samplingAttemptResult{}, attempts, err
		}
		if attempts > r.options.MaxModelRetries {
			return samplingAttemptResult{}, attempts, err
		}
		if eventErr := emitClientEvent(ctx, sink, model.ClientEvent{
			Type:         model.ClientEventModelRetry,
			TurnID:       turn.ID,
			RetryAttempt: attempts,
			RetryError:   err.Error(),
		}); eventErr != nil {
			return samplingAttemptResult{}, attempts, fmt.Errorf("emit retry client event: %w", eventErr)
		}
		metrics.RecordSamplingRetry()
		if backoff := retryBackoff(r.options, attempts, err); backoff > 0 {
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return samplingAttemptResult{}, attempts, ctx.Err()
			case <-timer.C:
			}
		}
	}
}

func materializeAttemptItems(attempt samplingAttemptResult) []model.Item {
	items := make([]model.Item, 0, len(attempt.items)+len(attempt.toolResults))
	for _, item := range attempt.items {
		items = append(items, cloneItem(item))
	}
	for _, result := range attempt.toolResults {
		items = append(items, cloneItem(result.item))
	}
	return items
}

func isTurnCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func appendCommittedItems(prefix []model.Item, suffix []model.Item) []model.Item {
	items := make([]model.Item, 0, len(prefix)+len(suffix))
	for _, item := range prefix {
		items = append(items, cloneItem(item))
	}
	for _, item := range suffix {
		items = append(items, cloneItem(item))
	}
	return items
}

func (r *Runner) receiveSamplingAttempt(
	ctx context.Context,
	turn model.Turn,
	stream EventStream,
	sink events.Sink,
	turnOptions TurnOptions,
	metrics *turnMetricsState,
	guardrailDenials *guardrailDenialCircuitBreaker,
) (samplingAttemptResult, error) {
	var (
		completedSeen bool
		attempt       samplingAttemptResult
		reasoningText strings.Builder
		assistantText strings.Builder
	)
	assistantParser := model.NewAssistantTextParser()
	var assistantCitation *model.MemoryCitation
	assistantItemID := ""
	progress := newProgressNarrator(r.options.ProgressNarration, sink, turn.ID, r.now)
	if progress != nil {
		defer progress.Close()
	}
	stopModelProgress := func() {}
	if progress != nil {
		stopModelProgress = progress.StartWork(
			ctx,
			model.WorkPhaseWaitingForModel,
			model.ProgressHint{Label: "Waiting for model"},
			"",
		)
	}
	modelProgressStopped := false
	stopModelProgressOnce := func() {
		if modelProgressStopped {
			return
		}
		modelProgressStopped = true
		stopModelProgress()
	}
	inFlightTools := newInFlightToolDispatcher(
		r,
		turn,
		sink,
		progress,
		turnOptions.PendingInputActivity,
		guardrailDenials,
	)

	for {
		event, err := stream.Recv()
		if err != nil {
			if err == io.EOF && completedSeen {
				break
			}
			metrics.EndSampling()
			// Codex starts completed tool calls while the response stream is
			// still open. If the stream later fails, those goroutines must be
			// drained before retry/error propagation so Dexco never leaks tool
			// work. If cancellation aborts an in-flight tool, keep its synthetic
			// aborted result on the attempt so the caller can persist Codex's
			// interrupted-turn transcript.
			metrics.BeginToolBlocking()
			toolResults, drainErr := inFlightTools.Drain()
			metrics.EndToolBlocking()
			attempt.toolResults = toolResults
			if drainErr != nil {
				return attempt, drainErr
			}
			// Codex treats any stream termination before response.completed as a
			// retryable stream failure. Dexco wraps it so protocol/sink errors do
			// not get retried accidentally.
			return attempt, streamReceiveError{
				err: fmt.Errorf("receive stream event: %w", err),
			}
		}

		if rawSink, ok := sink.(events.ResponseEventSink); ok {
			if err := rawSink.OnResponseEvent(ctx, turn.ID, event); err != nil {
				return samplingAttemptResult{}, fmt.Errorf("emit response event: %w", err)
			}
		}
		if err := emitClientEvent(ctx, sink, model.ClientEvent{
			Type:          model.ClientEventResponseEvent,
			TurnID:        turn.ID,
			ResponseEvent: &event,
		}); err != nil {
			return samplingAttemptResult{}, fmt.Errorf("emit response client event: %w", err)
		}
		if attempt.turnState == "" && event.TurnState != "" {
			// Codex learns this from transport metadata (`x-codex-turn-state`)
			// rather than model-visible content. Dexco lets any normalized
			// event carry it so HTTP and websocket adapters can share one loop
			// rule: capture the first value produced by this sampling attempt.
			attempt.turnState = event.TurnState
		}

		switch event.Type {
		case model.EventCreated,
			model.EventToolCallInputDelta,
			model.EventServerModel,
			model.EventModelVerifications,
			model.EventTurnModerationMetadata,
			model.EventSafetyBuffering,
			model.EventServerReasoningIncluded,
			model.EventRateLimits,
			model.EventModelsEtag:
			continue

		case model.EventOutputItemAdded:
			stopModelProgressOnce()
			if event.ItemID != "" && event.Item != nil && event.Item.Kind == model.ItemAssistantMessage {
				assistantItemID = event.ItemID
			}
			if event.Item != nil && event.Item.Kind == model.ItemAssistantMessage && event.Item.Content != "" {
				// Codex seeds assistant stream parsers from response.output_item.added
				// text, then appends later deltas. Dexco has a simpler text model
				// but preserves the same model-visible invariant: seeded text and
				// deltas become one final assistant message, not separate items.
				chunk := assistantParser.Push(event.Item.Content)
				assistantCitation = model.MergeMemoryCitations(assistantCitation, chunk.MemoryCitation)
				if err := emitAssistantVisibleText(ctx, sink, turn.ID, assistantItemID, chunk.VisibleText, &assistantText, metrics); err != nil {
					return samplingAttemptResult{}, fmt.Errorf("emit seeded text: %w", err)
				}
			}

		case model.EventOutputTextDelta:
			stopModelProgressOnce()
			if event.ItemID != "" {
				assistantItemID = event.ItemID
			}
			chunk := assistantParser.Push(event.Delta)
			assistantCitation = model.MergeMemoryCitations(assistantCitation, chunk.MemoryCitation)
			if err := emitAssistantVisibleText(ctx, sink, turn.ID, assistantItemID, chunk.VisibleText, &assistantText, metrics); err != nil {
				return samplingAttemptResult{}, fmt.Errorf("emit text delta: %w", err)
			}

		case model.EventReasoningDelta,
			model.EventReasoningSummaryDelta,
			model.EventReasoningContentDelta:
			stopModelProgressOnce()
			if event.Delta != "" {
				metrics.RecordFirstOutput()
			}
			if err := sink.OnReasoningDelta(ctx, turn.ID, event.Delta); err != nil {
				return samplingAttemptResult{}, fmt.Errorf("emit reasoning delta: %w", err)
			}
			if err := emitClientEvent(ctx, sink, model.ClientEvent{
				Type:   model.ClientEventReasoning,
				TurnID: turn.ID,
				ItemID: event.ItemID,
				Delta:  event.Delta,
			}); err != nil {
				return samplingAttemptResult{}, fmt.Errorf("emit reasoning client event: %w", err)
			}
			reasoningText.WriteString(event.Delta)

		case model.EventReasoningSummaryPartAdded:
			continue

		case model.EventOutputItemDone:
			stopModelProgressOnce()
			if event.Item == nil {
				metrics.EndSampling()
				return samplingAttemptResult{}, fmt.Errorf("output item done: missing item")
			}
			item := cloneItem(*event.Item)
			if item.Kind == model.ItemAssistantMessage {
				if event.ItemID != "" {
					assistantItemID = event.ItemID
				}
				tail := assistantParser.Finish()
				assistantCitation = model.MergeMemoryCitations(assistantCitation, tail.MemoryCitation)
				if err := emitAssistantVisibleText(ctx, sink, turn.ID, assistantItemID, tail.VisibleText, &assistantText, metrics); err != nil {
					return samplingAttemptResult{}, fmt.Errorf("emit finished assistant text: %w", err)
				}
				item = model.NormalizeAssistantMessageItem(item)
				if item.Content == "" && assistantText.Len() > 0 {
					item.Content = assistantText.String()
				}
				if item.MemoryCitation == nil {
					item.MemoryCitation = model.CloneMemoryCitation(assistantCitation)
				}
				assistantParser = model.NewAssistantTextParser()
				assistantCitation = nil
				assistantItemID = ""
				assistantText.Reset()
			}
			metrics.RecordFirstOutputForItem(item)
			attempt.items = append(attempt.items, item)
			if item.Kind == model.ItemToolCall && item.ToolCall != nil {
				if err := sink.OnToolCall(ctx, turn.ID, *item.ToolCall); err != nil {
					return samplingAttemptResult{}, fmt.Errorf("emit tool call: %w", err)
				}
				if err := emitClientEvent(ctx, sink, model.ClientEvent{
					Type:     model.ClientEventToolCall,
					TurnID:   turn.ID,
					ToolCall: item.ToolCall,
				}); err != nil {
					return samplingAttemptResult{}, fmt.Errorf("emit tool call client event: %w", err)
				}
				attempt.needsFollowUp = true
				inFlightTools.Start(ctx, *item.ToolCall)
			}
			if item.Kind == model.ItemWebSearch && item.WebSearch != nil {
				// Codex maps hosted web-search calls into first-class turn items.
				// Dexco model clients already provide normalized events, so the
				// runner's responsibility is to preserve and surface them instead
				// of treating the provider-only item as opaque metadata.
				webSearch := cloneWebSearch(*item.WebSearch)
				if err := emitClientEvent(ctx, sink, model.ClientEvent{
					Type:      model.ClientEventWebSearch,
					TurnID:    turn.ID,
					WebSearch: &webSearch,
				}); err != nil {
					return samplingAttemptResult{}, fmt.Errorf("emit web search client event: %w", err)
				}
			}
			if item.Kind == model.ItemHookPrompt && item.HookPrompt != nil {
				// Codex event_mapping exposes hook prompts as distinct turn
				// items, hiding surrounding contextual fragments. Dexco receives
				// normalized hook prompt items and preserves the same client
				// event/history distinction.
				hookPrompt := cloneHookPrompt(*item.HookPrompt)
				if err := emitClientEvent(ctx, sink, model.ClientEvent{
					Type:       model.ClientEventHookPrompt,
					TurnID:     turn.ID,
					HookPrompt: &hookPrompt,
				}); err != nil {
					return samplingAttemptResult{}, fmt.Errorf("emit hook prompt client event: %w", err)
				}
			}
			if item.Kind == model.ItemImageGeneration && item.ImageGeneration != nil {
				// Codex maps hosted image_generation_call output into a
				// first-class turn item and emits image-generation begin/end
				// events. Dexco keeps the portable part: model adapters provide a
				// normalized item, the runner commits it to history, and clients
				// receive the structured result even when artifact saving is left
				// to the embedding app.
				imageGeneration := cloneImageGeneration(*item.ImageGeneration)
				if err := emitClientEvent(ctx, sink, model.ClientEvent{
					Type:            model.ClientEventImageGeneration,
					TurnID:          turn.ID,
					ImageGeneration: &imageGeneration,
				}); err != nil {
					return samplingAttemptResult{}, fmt.Errorf("emit image generation client event: %w", err)
				}
			}
			if item.Kind == model.ItemAssistantMessage {
				attempt.finalMessage = item.Content
			}
			if item.Kind == model.ItemReasoning {
				reasoningText.Reset()
			}

		case model.EventCompleted:
			stopModelProgressOnce()
			completedSeen = true
			metrics.EndSampling()
			if event.TokenUsage != nil {
				attempt.tokenUsages = append(attempt.tokenUsages, *event.TokenUsage)
			}
			if event.EndTurn != nil && !*event.EndTurn {
				// Responses can ask the client to continue even when no tool call
				// was emitted. Codex follows up with another model request.
				attempt.needsFollowUp = true
			}
			break

		default:
			metrics.EndSampling()
			return samplingAttemptResult{}, fmt.Errorf("unsupported event type %q", event.Type)
		}

		if completedSeen {
			break
		}
	}

	if !completedSeen {
		metrics.EndSampling()
		return samplingAttemptResult{}, fmt.Errorf("stream ended before completed event")
	}
	metrics.BeginToolBlocking()
	toolResults, err := inFlightTools.Drain()
	metrics.EndToolBlocking()
	attempt.toolResults = toolResults
	if err != nil {
		return attempt, err
	}
	if reasoningText.Len() > 0 {
		attempt.items = append(attempt.items, model.ReasoningItem(reasoningText.String()))
	}
	tail := assistantParser.Finish()
	assistantCitation = model.MergeMemoryCitations(assistantCitation, tail.MemoryCitation)
	if err := emitAssistantVisibleText(ctx, sink, turn.ID, assistantItemID, tail.VisibleText, &assistantText, metrics); err != nil {
		return samplingAttemptResult{}, fmt.Errorf("emit finished assistant text: %w", err)
	}
	if assistantText.Len() > 0 && attempt.finalMessage == "" {
		message := model.AssistantMessageItem(assistantText.String())
		message.MemoryCitation = model.MergeMemoryCitations(message.MemoryCitation, assistantCitation)
		attempt.items = append(attempt.items, message)
		attempt.finalMessage = message.Content
	}
	return attempt, nil
}

func emitAssistantVisibleText(
	ctx context.Context,
	sink events.Sink,
	turnID string,
	itemID string,
	visibleText string,
	assistantText *strings.Builder,
	metrics *turnMetricsState,
) error {
	if visibleText == "" {
		return nil
	}
	metrics.RecordFirstMessage()
	if err := sink.OnTextDelta(ctx, turnID, visibleText); err != nil {
		return err
	}
	if err := emitClientEvent(ctx, sink, model.ClientEvent{
		Type:   model.ClientEventTextDelta,
		TurnID: turnID,
		ItemID: itemID,
		Delta:  visibleText,
	}); err != nil {
		return fmt.Errorf("emit text client event: %w", err)
	}
	assistantText.WriteString(visibleText)
	return nil
}

func isStreamReceiveError(err error) bool {
	_, ok := err.(streamReceiveError)
	return ok
}

func retryBackoff(options Options, attempt int, err error) time.Duration {
	if options.RetryBackoff == nil {
		return 0
	}
	return options.RetryBackoff(attempt, err)
}

func (r *Runner) now() time.Time {
	if r.options.Clock != nil {
		return r.options.Clock()
	}
	return time.Now()
}

type turnMetricsState struct {
	now             func() time.Time
	startedAt       time.Time
	samplingStarted time.Time
	toolStarted     time.Time
	lastActivityEnd time.Time
	samplingActive  bool
	toolActive      bool
	metrics         model.TurnMetrics
}

func newTurnMetricsState(now func() time.Time) *turnMetricsState {
	startedAt := now()
	return &turnMetricsState{
		now:             now,
		startedAt:       startedAt,
		lastActivityEnd: startedAt,
		metrics: model.TurnMetrics{
			StartedAt: startedAt,
		},
	}
}

func (m *turnMetricsState) BeginSampling() {
	if m == nil || m.samplingActive {
		return
	}
	now := m.now()
	if m.metrics.Profile.SamplingRequestCount == 0 {
		m.metrics.Profile.BeforeFirstSampling += now.Sub(m.startedAt)
	} else {
		m.metrics.Profile.BetweenSamplingOverhead += now.Sub(m.lastActivityEnd)
	}
	m.metrics.Profile.SamplingRequestCount++
	m.samplingStarted = now
	m.samplingActive = true
}

func (m *turnMetricsState) EndSampling() {
	if m == nil || !m.samplingActive {
		return
	}
	now := m.now()
	m.metrics.Profile.Sampling += now.Sub(m.samplingStarted)
	m.lastActivityEnd = now
	m.samplingActive = false
}

func (m *turnMetricsState) BeginToolBlocking() {
	if m == nil || m.toolActive {
		return
	}
	m.toolStarted = m.now()
	m.toolActive = true
}

func (m *turnMetricsState) EndToolBlocking() {
	if m == nil || !m.toolActive {
		return
	}
	now := m.now()
	m.metrics.Profile.ToolBlocking += now.Sub(m.toolStarted)
	m.lastActivityEnd = now
	m.toolActive = false
}

func (m *turnMetricsState) RecordSamplingRetry() {
	if m == nil {
		return
	}
	m.metrics.Profile.SamplingRetryCount++
}

func (m *turnMetricsState) RecordFirstOutput() {
	if m == nil || m.metrics.HasFirstOutput {
		return
	}
	m.metrics.HasFirstOutput = true
	m.metrics.TimeToFirstOutput = m.now().Sub(m.startedAt)
}

func (m *turnMetricsState) RecordFirstMessage() {
	if m == nil {
		return
	}
	m.RecordFirstOutput()
	if m.metrics.HasFirstMessage {
		return
	}
	m.metrics.HasFirstMessage = true
	m.metrics.TimeToFirstMessage = m.now().Sub(m.startedAt)
}

func (m *turnMetricsState) RecordFirstOutputForItem(item model.Item) {
	if m == nil {
		return
	}
	switch item.Kind {
	case model.ItemAssistantMessage:
		if item.Content != "" {
			m.RecordFirstMessage()
		}
	case model.ItemReasoning:
		if item.Content != "" {
			m.RecordFirstOutput()
		}
	case model.ItemToolCall, model.ItemWebSearch, model.ItemImageGeneration:
		m.RecordFirstOutput()
	case model.ItemContext,
		model.ItemUserInput,
		model.ItemToolResult,
		model.ItemHookPrompt:
		return
	}
}

func (m *turnMetricsState) Complete() model.TurnMetrics {
	if m == nil {
		return model.TurnMetrics{}
	}
	m.EndSampling()
	m.EndToolBlocking()
	now := m.now()
	m.metrics.Profile.AfterLastSampling += now.Sub(m.lastActivityEnd)
	return m.metrics
}

func isNonRetryableModelError(err error) bool {
	var modelErr *model.ModelError
	return errors.As(err, &modelErr) && !modelErr.Retryable
}

func newInFlightToolDispatcher(
	runner *Runner,
	turn model.Turn,
	sink events.Sink,
	progress *progressNarrator,
	pendingInputActivity PendingInputActivityFunc,
	guardrailDenials *guardrailDenialCircuitBreaker,
) *inFlightToolDispatcher {
	return &inFlightToolDispatcher{
		runner:               runner,
		turn:                 turn,
		sink:                 sink,
		pendingInputActivity: pendingInputActivity,
		guardrailDenials:     guardrailDenials,
		progress:             progress,
	}
}

func (d *inFlightToolDispatcher) Start(ctx context.Context, call model.ToolCall) {
	call = cloneToolCall(call)
	resultChannel := make(chan inFlightToolResult, 1)
	d.pending = append(d.pending, inFlightToolCall{
		call:   call,
		result: resultChannel,
	})

	go func() {
		result, err := d.dispatch(ctx, call)
		resultChannel <- inFlightToolResult{result: result, err: err}
	}()
}

func (d *inFlightToolDispatcher) Drain() ([]toolDispatchResult, error) {
	if len(d.pending) == 0 {
		return nil, nil
	}
	results := make([]toolDispatchResult, 0, len(d.pending))
	var firstErr error
	for _, pending := range d.pending {
		outcome := <-pending.result
		if outcome.err != nil {
			if firstErr == nil {
				firstErr = outcome.err
			}
			if isTurnCancellation(outcome.err) {
				// Codex records an aborted output for each tool call that had
				// already become model-visible. Drain every pending call before
				// returning so parallel/queued calls have deterministic aborted
				// history instead of depending on which goroutine reported first.
				results = append(results, abortedToolDispatchResult(pending.call))
			}
			continue
		}
		results = append(results, outcome.result)
	}
	d.pending = nil
	if firstErr != nil {
		return results, firstErr
	}
	return results, nil
}

func (d *inFlightToolDispatcher) Close() {
	if d == nil || d.progress == nil {
		return
	}
	d.progress.Close()
}

func abortedToolDispatchResult(call model.ToolCall) toolDispatchResult {
	return toolDispatchResult{
		item: model.ToolResultItem(model.ToolResult{
			CallID:  call.CallID,
			Name:    call.Name,
			Output:  "tool call aborted by turn cancellation; it may have partially executed",
			Success: false,
		}),
	}
}

func (d *inFlightToolDispatcher) dispatch(
	ctx context.Context,
	call model.ToolCall,
) (toolDispatchResult, error) {
	// Codex's ToolCallRuntime starts tool futures as soon as a completed
	// function-call item arrives, before response.completed. A shared async lock
	// then preserves the same safety model as post-completion batching:
	// parallel-safe tools may overlap, while serial tools exclude both other
	// serial tools and later parallel reads. Go's RWMutex gives Dexco the same
	// writer-preference behavior without introducing a broader scheduler.
	if d.runner.options.ParallelTools && d.runner.router.SupportsParallel(call) {
		d.lock.RLock()
		defer d.lock.RUnlock()
	} else {
		d.lock.Lock()
		defer d.lock.Unlock()
	}
	toolCtx, stopPendingInputWatcher := d.toolContext(ctx, call)
	defer stopPendingInputWatcher()
	return d.runner.dispatchOneToolCallWithToolContext(
		ctx,
		toolCtx,
		d.turn,
		d.sink,
		d.progress,
		call,
		d.guardrailDenials,
	)
}

func (d *inFlightToolDispatcher) toolContext(
	ctx context.Context,
	call model.ToolCall,
) (context.Context, func()) {
	if d.pendingInputActivity == nil || !d.runner.router.InterruptsOnPendingInput(call) {
		return ctx, func() {}
	}
	activity := d.pendingInputActivity(ctx)
	if activity == nil {
		return ctx, func() {}
	}
	toolCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		select {
		case <-activity:
			cancel()
		case <-toolCtx.Done():
		case <-done:
		}
	}()
	return toolCtx, func() {
		close(done)
		cancel()
	}
}

func (r *Runner) dispatchToolCalls(
	ctx context.Context,
	turn model.Turn,
	calls []model.ToolCall,
) ([]toolDispatchResult, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	if !r.options.ParallelTools {
		results := make([]toolDispatchResult, 0, len(calls))
		for _, call := range calls {
			result, err := r.dispatchOneToolCall(ctx, turn, call)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		}
		return results, nil
	}

	results := make([]toolDispatchResult, 0, len(calls))
	for index := 0; index < len(calls); {
		if !r.router.SupportsParallel(calls[index]) {
			result, err := r.dispatchOneToolCall(ctx, turn, calls[index])
			if err != nil {
				return nil, err
			}
			results = append(results, result)
			index++
			continue
		}

		start := index
		for index < len(calls) && r.router.SupportsParallel(calls[index]) {
			index++
		}
		// Codex batches adjacent parallel-safe tool calls. Dexco keeps the same
		// external history shape: all model tool-call items are already committed,
		// and all result items are appended afterward in call order.
		batch, err := r.dispatchParallelToolCalls(ctx, turn, calls[start:index])
		if err != nil {
			return nil, err
		}
		results = append(results, batch...)
	}
	return results, nil
}

func (r *Runner) dispatchParallelToolCalls(
	ctx context.Context,
	turn model.Turn,
	calls []model.ToolCall,
) ([]toolDispatchResult, error) {
	results := make([]toolDispatchResult, len(calls))
	errs := make([]error, len(calls))
	var wg sync.WaitGroup
	wg.Add(len(calls))
	for i, call := range calls {
		go func() {
			defer wg.Done()
			results[i], errs[i] = r.dispatchOneToolCall(ctx, turn, call)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("dispatch tool %q: %w", calls[i].Name, err)
		}
	}
	return results, nil
}

func (r *Runner) dispatchOneToolCall(
	ctx context.Context,
	turn model.Turn,
	call model.ToolCall,
) (toolDispatchResult, error) {
	return r.dispatchOneToolCallWithToolContext(ctx, ctx, turn, nil, nil, call, nil)
}

func (r *Runner) dispatchOneToolCallWithToolContext(
	ctx context.Context,
	toolCtx context.Context,
	turn model.Turn,
	sink events.Sink,
	progress *progressNarrator,
	call model.ToolCall,
	guardrailDenials *guardrailDenialCircuitBreaker,
) (toolDispatchResult, error) {
	ctx = permissionstore.ContextWithTurnID(ctx, turn.ID)
	toolCtx = permissionstore.ContextWithTurnID(toolCtx, turn.ID)
	if err := ctx.Err(); err != nil {
		return toolDispatchResult{}, err
	}
	if r.options.Hooks.BeforeToolCall != nil {
		// Codex reports hook failures against the original model-requested tool.
		// Do not trust the returned call when the hook returns an error; callers
		// commonly return a zero value alongside the error.
		toolName := call.Name
		var err error
		call, err = r.options.Hooks.BeforeToolCall(ctx, turn, call)
		if err != nil {
			return toolDispatchResult{}, fmt.Errorf("before tool call hook %q: %w", toolName, err)
		}
	}
	// Codex guardrail parity: normalize/mutate the call first, then classify and
	// approve it before invoking the side-effecting handler.
	stopPolicyProgress := func() {}
	if progress != nil {
		stopPolicyProgress = progress.StartWork(
			ctx,
			model.WorkPhaseCheckingPolicy,
			model.ProgressHint{Label: "Checking access"},
			call.Name,
		)
	}
	deniedResult, clientEvents, guardrail, err := r.reviewToolCall(ctx, turn, call)
	stopPolicyProgress()
	if err != nil {
		return toolDispatchResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return toolDispatchResult{}, err
	}
	if deniedResult != nil {
		if err := guardrailDenials.recordDenial(turn.ID); err != nil {
			return toolDispatchResult{}, err
		}
		// Rejections are returned as failed tool outputs rather than crashing the
		// loop, matching Codex's pattern of letting the model observe denial and
		// choose the next step.
		return toolDispatchResult{
			item:         model.ToolResultItem(*deniedResult),
			clientEvents: clientEvents,
		}, nil
	}
	guardrailDenials.recordNonDenial()
	stopProgress := func() {}
	if progress != nil && guardrail != nil {
		stopProgress = progress.StartTool(ctx, call, guardrail.ProgressHint)
	}
	defer stopProgress()
	if err := r.notifyToolLifecycle(ctx, turn, model.ToolLifecycleEvent{
		Phase: model.ToolLifecycleStart,
		Call:  call,
	}); err != nil {
		return toolDispatchResult{}, err
	}
	item, outcome, err := r.router.DispatchWithOutcome(toolCtx, call)
	if err != nil {
		finishErr := r.notifyToolLifecycle(ctx, turn, model.ToolLifecycleEvent{
			Phase:           model.ToolLifecycleFinish,
			Call:            call,
			Outcome:         model.ToolLifecycleOutcomeFailed,
			HandlerExecuted: outcome.HandlerExecuted,
		})
		if finishErr != nil {
			return toolDispatchResult{}, finishErr
		}
		return toolDispatchResult{}, fmt.Errorf("dispatch tool %q: %w", call.Name, err)
	}
	if err := r.notifyToolLifecycle(ctx, turn, toolLifecycleFinishEvent(call, item, outcome)); err != nil {
		return toolDispatchResult{}, err
	}
	if err := ctx.Err(); err != nil {
		// Codex interrupt parity: a cancellation while a side-effecting tool is
		// running aborts the turn instead of committing a successful or failed
		// tool result and continuing with another model request.
		return toolDispatchResult{}, err
	}
	if r.options.Hooks.AfterToolCall != nil {
		item, err = r.options.Hooks.AfterToolCall(ctx, turn, call, item)
		if err != nil {
			return toolDispatchResult{}, fmt.Errorf("after tool call hook %q: %w", call.Name, err)
		}
	}
	return toolDispatchResult{
		item:         item,
		clientEvents: clientEvents,
	}, nil
}

func (r *Runner) notifyToolLifecycle(
	ctx context.Context,
	turn model.Turn,
	event model.ToolLifecycleEvent,
) error {
	if r.options.Hooks.ToolLifecycle == nil {
		return nil
	}
	if err := r.options.Hooks.ToolLifecycle(ctx, turn, event); err != nil {
		return fmt.Errorf("tool lifecycle hook %q %q: %w", event.Phase, event.Call.Name, err)
	}
	return nil
}

func toolLifecycleFinishEvent(
	call model.ToolCall,
	item model.Item,
	outcome tools.DispatchOutcome,
) model.ToolLifecycleEvent {
	event := model.ToolLifecycleEvent{
		Phase:           model.ToolLifecycleFinish,
		Call:            call,
		HandlerExecuted: outcome.HandlerExecuted,
	}
	if outcome.HandlerError || !outcome.HandlerExecuted {
		event.Outcome = model.ToolLifecycleOutcomeFailed
	} else {
		event.Outcome = model.ToolLifecycleOutcomeCompleted
	}
	if item.ToolResult != nil {
		result := cloneToolResult(*item.ToolResult)
		event.Result = &result
		event.Success = result.Success
	}
	return event
}

func (r *Runner) reviewToolCall(
	ctx context.Context,
	turn model.Turn,
	call model.ToolCall,
) (*model.ToolResult, []model.ClientEvent, *model.ToolGuardrail, error) {
	guardrail, err := r.router.Guardrail(ctx, call)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("tool guardrail %q: %w", call.Name, err)
	}
	policy := r.options.Guardrails.ApprovalPolicy
	if policy == "" {
		policy = model.ApprovalPolicyAllowAll
	}

	needsApproval, deniedReason, err := approvalRequirement(policy, guardrail)
	if err != nil {
		return nil, nil, nil, err
	}
	request := toolApprovalRequest(turn, call, guardrail, policy)
	if deniedReason != "" {
		request.Reason = deniedReason
		result := deniedToolResultForRequest(call, request, deniedReason)
		return &result, []model.ClientEvent{
			approvalDecisionEvent(turn.ID, request, model.ApprovalDecisionDenied),
		}, nil, nil
	}
	if !needsApproval {
		return nil, nil, &guardrail, nil
	}
	if guardrail.PermissionGrantKey != "" &&
		r.options.Guardrails.PermissionGrants != nil &&
		r.options.Guardrails.PermissionGrants.Has(turn.ID, guardrail.PermissionGrantKey) &&
		!r.options.Guardrails.PermissionGrants.StrictAutoReviewForGrant(turn.ID, guardrail.PermissionGrantKey) {
		// Codex lets request_permissions grants preapprove later shell-like
		// calls in the same turn or session. Strict turn grants are different:
		// they prove the request is within the granted key, but Codex still sends
		// the concrete action through Guardian auto-review. Dexco preserves that
		// nuance by bypassing only non-strict grants and otherwise continuing
		// through the normal hook/reviewer/client-event path.
		return nil, nil, &guardrail, nil
	}

	// Emit approval lifecycle events separately from normal tool results so UI
	// or API clients can mirror Codex's request/decision surfaces without
	// scraping text from failed tool outputs.
	clientEvents := []model.ClientEvent{
		{
			Type:                model.ClientEventToolApprovalRequest,
			TurnID:              turn.ID,
			ToolApprovalRequest: &request,
		},
	}
	if r.options.Hooks.ReviewToolCall != nil {
		decision, err := r.options.Hooks.ReviewToolCall(ctx, turn, request)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("review tool call hook %q: %w", call.Name, err)
		}
		result, decided, err := approvalDecisionResult(call, request, decision, "permission hook denied tool call")
		if decided {
			clientEvents = append(clientEvents, approvalDecisionEvent(turn.ID, request, decision))
		}
		if err != nil || decided {
			if result != nil || err != nil {
				return result, clientEvents, nil, err
			}
			return nil, clientEvents, &guardrail, nil
		}
	}

	if r.options.Guardrails.Reviewer == nil {
		// Safe default for opt-in guardrails: if policy says approval is required
		// and nobody approves, do not run the tool.
		result := deniedToolResultForRequest(call, request, "approval required but no reviewer approved the tool call")
		clientEvents = append(
			clientEvents,
			approvalDecisionEvent(turn.ID, request, model.ApprovalDecisionDenied),
		)
		return &result, clientEvents, nil, nil
	}

	decision, err := r.options.Guardrails.Reviewer(ctx, turn, request)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("review tool call %q: %w", call.Name, err)
	}
	result, decided, err := approvalDecisionResult(call, request, decision, "reviewer denied tool call")
	if decided {
		clientEvents = append(clientEvents, approvalDecisionEvent(turn.ID, request, decision))
	}
	if err != nil {
		return nil, nil, nil, err
	}
	if !decided {
		result := deniedToolResultForRequest(call, request, "reviewer did not approve the tool call")
		clientEvents = append(
			clientEvents,
			approvalDecisionEvent(turn.ID, request, model.ApprovalDecisionDenied),
		)
		return &result, clientEvents, nil, nil
	}
	if result != nil {
		return result, clientEvents, nil, nil
	}
	return nil, clientEvents, &guardrail, nil
}

func toolApprovalRequest(
	turn model.Turn,
	call model.ToolCall,
	guardrail model.ToolGuardrail,
	policy model.ApprovalPolicy,
) model.ToolApprovalRequest {
	return model.ToolApprovalRequest{
		TurnID:    turn.ID,
		Call:      call,
		Guardrail: guardrail,
		Policy:    policy,
		Reason:    approvalReason(guardrail, policy),
	}
}

func approvalDecisionEvent(
	turnID string,
	request model.ToolApprovalRequest,
	decision model.ApprovalDecision,
) model.ClientEvent {
	var toolPolicyDecision *model.ToolPolicyDecision
	if request.Guardrail.ToolPolicyDecision != nil {
		decisionPayload := cloneToolPolicyDecision(*request.Guardrail.ToolPolicyDecision)
		decisionPayload.Decision = decision
		if decisionPayload.ToolName == "" {
			decisionPayload.ToolName = request.Call.Name
		}
		if decision == model.ApprovalDecisionDenied && decisionPayload.ReasonCode == "" {
			decisionPayload.ReasonCode = "policy_denied"
		}
		toolPolicyDecision = &decisionPayload
	}
	return model.ClientEvent{
		Type:                model.ClientEventToolApprovalDecision,
		TurnID:              turnID,
		ToolApprovalRequest: &request,
		ApprovalDecision:    decision,
		ToolPolicyDecision:  toolPolicyDecision,
	}
}

func approvalRequirement(
	policy model.ApprovalPolicy,
	guardrail model.ToolGuardrail,
) (bool, string, error) {
	if guardrail.ApprovalRequirement == model.ApprovalRequirementDenied {
		return false, approvalReason(guardrail, policy), nil
	}

	switch policy {
	case model.ApprovalPolicyAllowAll:
		return false, "", nil
	case model.ApprovalPolicyRequireForSensitive:
		return guardrail.ApprovalRequirement == model.ApprovalRequirementRequired, "", nil
	case model.ApprovalPolicyRequireForAll:
		return true, "", nil
	case model.ApprovalPolicyDenyAll:
		return false, "tool execution disabled by guardrail policy", nil
	default:
		return false, "", fmt.Errorf("unknown approval policy %q", policy)
	}
}

func approvalDecisionResult(
	call model.ToolCall,
	request model.ToolApprovalRequest,
	decision model.ApprovalDecision,
	deniedReason string,
) (*model.ToolResult, bool, error) {
	switch decision {
	case model.ApprovalDecisionApproved:
		return nil, true, nil
	case model.ApprovalDecisionDenied:
		result := deniedToolResultForRequest(call, request, deniedReason)
		return &result, true, nil
	case model.ApprovalDecisionNoDecision, "":
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("unknown approval decision %q", decision)
	}
}

func approvalReason(guardrail model.ToolGuardrail, policy model.ApprovalPolicy) string {
	if guardrail.Reason != "" {
		return guardrail.Reason
	}
	if policy == model.ApprovalPolicyRequireForAll {
		return "approval required for every tool call"
	}
	if guardrail.Risk != "" && guardrail.Risk != model.ToolRiskUnknown {
		return fmt.Sprintf("tool risk %q requires approval", guardrail.Risk)
	}
	return "tool call requires approval"
}

func deniedToolResult(call model.ToolCall, reason string) model.ToolResult {
	if reason == "" {
		reason = "tool call denied"
	}
	return model.ToolResult{
		CallID:  call.CallID,
		Name:    call.Name,
		Output:  fmt.Sprintf("tool call denied by guardrail: %s", reason),
		Success: false,
	}
}

func deniedToolResultForRequest(
	call model.ToolCall,
	request model.ToolApprovalRequest,
	reason string,
) model.ToolResult {
	if request.Guardrail.ToolPolicyDecision != nil {
		return model.ToolResult{
			CallID:  call.CallID,
			Name:    call.Name,
			Output:  "This action is not allowed for the current user.",
			Success: false,
		}
	}
	return deniedToolResult(call, reason)
}

func emitClientEvent(ctx context.Context, sink events.Sink, event model.ClientEvent) error {
	clientSink, ok := sink.(events.ClientEventSink)
	if !ok {
		return nil
	}
	return clientSink.OnClientEvent(ctx, event)
}

type progressNarrator struct {
	cfg    model.ProgressNarrationConfig
	sink   events.Sink
	turnID string
	now    func() time.Time

	mu         sync.Mutex
	generation int
	closed     bool
	active     map[string]activeProgress
}

type activeProgress struct {
	call      model.ToolCall
	phase     model.WorkPhase
	hint      model.ProgressHint
	startedAt time.Time
}

func newProgressNarrator(
	cfg model.ProgressNarrationConfig,
	sink events.Sink,
	turnID string,
	now func() time.Time,
) *progressNarrator {
	if !cfg.Enabled || cfg.InitialDelay <= 0 {
		return nil
	}
	if _, ok := sink.(events.ClientEventSink); !ok {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	return &progressNarrator{
		cfg:    cfg,
		sink:   sink,
		turnID: turnID,
		now:    now,
		active: make(map[string]activeProgress),
	}
}

func (n *progressNarrator) StartTool(
	ctx context.Context,
	call model.ToolCall,
	hint *model.ProgressHint,
) func() {
	progressHint := model.ProgressHint{Label: "Running tool"}
	if hint != nil {
		progressHint = *hint
	}
	return n.start(ctx, model.WorkPhaseRunningTool, call, progressHint)
}

func (n *progressNarrator) StartWork(
	ctx context.Context,
	phase model.WorkPhase,
	hint model.ProgressHint,
	toolName string,
) func() {
	return n.start(ctx, phase, model.ToolCall{Name: toolName}, hint)
}

func (n *progressNarrator) start(
	ctx context.Context,
	phase model.WorkPhase,
	call model.ToolCall,
	hint model.ProgressHint,
) func() {
	if n == nil {
		return func() {}
	}
	if phase == "" {
		phase = model.WorkPhaseRunningTool
	}
	progressHint := sanitizeProgressHint(hint)
	key := call.CallID
	if key == "" {
		key = call.Name
	}
	if key == "" {
		key = string(phase)
	}

	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return func() {}
	}
	n.active[key] = activeProgress{
		call:      cloneToolCall(call),
		phase:     phase,
		hint:      progressHint,
		startedAt: n.now(),
	}
	n.generation++
	generation := n.generation
	n.mu.Unlock()

	n.schedule(ctx, generation, n.cfg.InitialDelay)

	var once sync.Once
	return func() {
		once.Do(func() {
			n.mu.Lock()
			if n.closed {
				n.mu.Unlock()
				return
			}
			delete(n.active, key)
			n.generation++
			generation := n.generation
			hasActive := len(n.active) > 0
			n.mu.Unlock()
			if hasActive {
				n.schedule(ctx, generation, n.cfg.InitialDelay)
			}
		})
	}
}

func (n *progressNarrator) Close() {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.closed = true
	n.generation++
	n.active = nil
	n.mu.Unlock()
}

func (n *progressNarrator) schedule(ctx context.Context, generation int, delay time.Duration) {
	if n == nil || delay <= 0 {
		return
	}
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		for {
			event, ok := n.event(generation)
			if !ok {
				return
			}
			if err := emitClientEvent(ctx, n.sink, event); err != nil {
				return
			}
			if n.cfg.RepeatInterval <= 0 {
				return
			}
			timer.Reset(n.cfg.RepeatInterval)
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
	}()
}

func (n *progressNarrator) event(generation int) (model.ClientEvent, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed || generation != n.generation || len(n.active) == 0 {
		return model.ClientEvent{}, false
	}

	active := make([]activeProgress, 0, len(n.active))
	for _, item := range n.active {
		active = append(active, item)
	}
	progress := progressNarrationForActive(active, n.now())
	return model.ClientEvent{
		Type:              model.ClientEventProgressNarration,
		TurnID:            n.turnID,
		ProgressNarration: &progress,
	}, true
}

func progressNarrationForActive(active []activeProgress, now time.Time) model.ProgressNarration {
	if len(active) == 1 {
		item := active[0]
		message := progressMessage(item.hint)
		return model.ProgressNarration{
			Phase:     item.phase,
			Message:   message,
			Label:     item.hint.Label,
			Detail:    item.hint.Detail,
			ToolName:  item.call.Name,
			StartedAt: item.startedAt,
			Elapsed:   now.Sub(item.startedAt),
		}
	}

	startedAt := active[0].startedAt
	sharedLabel := active[0].hint.Label
	allShareLabel := sharedLabel != ""
	for _, item := range active[1:] {
		if item.startedAt.Before(startedAt) {
			startedAt = item.startedAt
		}
		if item.hint.Label != sharedLabel {
			allShareLabel = false
		}
	}
	message := fmt.Sprintf("Running %d tasks", len(active))
	label := ""
	if allShareLabel {
		message = sharedLabel
		label = sharedLabel
	}
	return model.ProgressNarration{
		Phase:     model.WorkPhaseWaitingParallel,
		Message:   truncateProgressText(message, 96),
		Label:     truncateProgressText(label, 48),
		StartedAt: startedAt,
		Elapsed:   now.Sub(startedAt),
	}
}

func sanitizeProgressHint(hint model.ProgressHint) model.ProgressHint {
	hint.Label = truncateProgressText(strings.TrimSpace(hint.Label), 48)
	hint.Detail = truncateProgressText(strings.TrimSpace(hint.Detail), 64)
	if hint.Label == "" {
		hint.Label = "Running tool"
	}
	return hint
}

func progressMessage(hint model.ProgressHint) string {
	if hint.Detail == "" {
		return truncateProgressText(hint.Label, 96)
	}
	return truncateProgressText(strings.TrimSpace(hint.Label+" "+hint.Detail), 96)
}

func truncateProgressText(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

type bufferedSink struct {
	target events.Sink
	calls  []func(context.Context) error
}

func newBufferedSink(target events.Sink) *bufferedSink {
	return &bufferedSink{target: target}
}

func (s *bufferedSink) Flush(ctx context.Context) error {
	for _, call := range s.calls {
		if err := call(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *bufferedSink) OnTurnStarted(context.Context, model.Turn) error {
	return nil
}

func (s *bufferedSink) OnTextDelta(_ context.Context, turnID string, delta string) error {
	s.calls = append(s.calls, func(ctx context.Context) error {
		return s.target.OnTextDelta(ctx, turnID, delta)
	})
	return nil
}

func (s *bufferedSink) OnReasoningDelta(_ context.Context, turnID string, delta string) error {
	s.calls = append(s.calls, func(ctx context.Context) error {
		return s.target.OnReasoningDelta(ctx, turnID, delta)
	})
	return nil
}

func (s *bufferedSink) OnToolCall(_ context.Context, turnID string, call model.ToolCall) error {
	call = cloneToolCall(call)
	s.calls = append(s.calls, func(ctx context.Context) error {
		return s.target.OnToolCall(ctx, turnID, call)
	})
	return nil
}

func (s *bufferedSink) OnToolResult(_ context.Context, turnID string, result model.ToolResult) error {
	result = cloneToolResult(result)
	s.calls = append(s.calls, func(ctx context.Context) error {
		return s.target.OnToolResult(ctx, turnID, result)
	})
	return nil
}

func (s *bufferedSink) OnTurnCompleted(context.Context, model.Turn) error {
	return nil
}

func (s *bufferedSink) OnResponseEvent(
	_ context.Context,
	turnID string,
	event model.ResponseEvent,
) error {
	rawSink, ok := s.target.(events.ResponseEventSink)
	if !ok {
		return nil
	}
	event = cloneResponseEvent(event)
	s.calls = append(s.calls, func(ctx context.Context) error {
		return rawSink.OnResponseEvent(ctx, turnID, event)
	})
	return nil
}

func (s *bufferedSink) OnClientEvent(_ context.Context, event model.ClientEvent) error {
	clientSink, ok := s.target.(events.ClientEventSink)
	if !ok {
		return nil
	}
	event = cloneClientEvent(event)
	s.calls = append(s.calls, func(ctx context.Context) error {
		return clientSink.OnClientEvent(ctx, event)
	})
	return nil
}

func cloneClientEvent(event model.ClientEvent) model.ClientEvent {
	if event.Turn != nil {
		turn := *event.Turn
		turn.History = cloneItems(turn.History)
		event.Turn = &turn
	}
	if event.ToolCall != nil {
		toolCall := cloneToolCall(*event.ToolCall)
		event.ToolCall = &toolCall
	}
	if event.ToolResult != nil {
		toolResult := cloneToolResult(*event.ToolResult)
		event.ToolResult = &toolResult
	}
	if event.WebSearch != nil {
		webSearch := cloneWebSearch(*event.WebSearch)
		event.WebSearch = &webSearch
	}
	if event.HookPrompt != nil {
		hookPrompt := cloneHookPrompt(*event.HookPrompt)
		event.HookPrompt = &hookPrompt
	}
	if event.ImageGeneration != nil {
		imageGeneration := cloneImageGeneration(*event.ImageGeneration)
		event.ImageGeneration = &imageGeneration
	}
	if event.PlanUpdate != nil {
		planUpdate := clonePlanUpdate(*event.PlanUpdate)
		event.PlanUpdate = &planUpdate
	}
	if event.ToolApprovalRequest != nil {
		toolApprovalRequest := cloneToolApprovalRequest(*event.ToolApprovalRequest)
		event.ToolApprovalRequest = &toolApprovalRequest
	}
	if event.ToolPolicyDecision != nil {
		toolPolicyDecision := cloneToolPolicyDecision(*event.ToolPolicyDecision)
		event.ToolPolicyDecision = &toolPolicyDecision
	}
	if event.ProgressNarration != nil {
		progressNarration := *event.ProgressNarration
		event.ProgressNarration = &progressNarration
	}
	if event.ResponseEvent != nil {
		responseEvent := cloneResponseEvent(*event.ResponseEvent)
		event.ResponseEvent = &responseEvent
	}
	return event
}

func cloneToolApprovalRequest(request model.ToolApprovalRequest) model.ToolApprovalRequest {
	request.Call = cloneToolCall(request.Call)
	request.Guardrail = cloneToolGuardrail(request.Guardrail)
	return request
}

func cloneToolGuardrail(guardrail model.ToolGuardrail) model.ToolGuardrail {
	if guardrail.Metadata != nil {
		metadata := make(map[string]any, len(guardrail.Metadata))
		for key, value := range guardrail.Metadata {
			metadata[key] = value
		}
		guardrail.Metadata = metadata
	}
	if guardrail.ToolPolicyDecision != nil {
		decision := cloneToolPolicyDecision(*guardrail.ToolPolicyDecision)
		guardrail.ToolPolicyDecision = &decision
	}
	if guardrail.ProgressHint != nil {
		progressHint := *guardrail.ProgressHint
		guardrail.ProgressHint = &progressHint
	}
	return guardrail
}

func cloneToolPolicyDecision(decision model.ToolPolicyDecision) model.ToolPolicyDecision {
	decision.RequiredCapabilities.All = append([]string(nil), decision.RequiredCapabilities.All...)
	decision.RequiredCapabilities.Any = append([]string(nil), decision.RequiredCapabilities.Any...)
	return decision
}

func cloneResponseEvent(event model.ResponseEvent) model.ResponseEvent {
	if event.Item != nil {
		item := cloneItem(*event.Item)
		event.Item = &item
	}
	if event.SummaryIndex != nil {
		value := *event.SummaryIndex
		event.SummaryIndex = &value
	}
	if event.ContentIndex != nil {
		value := *event.ContentIndex
		event.ContentIndex = &value
	}
	if event.EndTurn != nil {
		value := *event.EndTurn
		event.EndTurn = &value
	}
	if event.TokenUsage != nil {
		tokenUsage := *event.TokenUsage
		event.TokenUsage = &tokenUsage
	}
	if event.Metadata != nil {
		metadata := make(map[string]any, len(event.Metadata))
		for key, value := range event.Metadata {
			metadata[key] = value
		}
		event.Metadata = metadata
	}
	return event
}

func cloneToolCall(call model.ToolCall) model.ToolCall {
	call.Arguments = append([]byte(nil), call.Arguments...)
	return call
}

func cloneToolResult(result model.ToolResult) model.ToolResult {
	result.Parts = cloneContentParts(result.Parts)
	if result.PlanUpdate != nil {
		planUpdate := clonePlanUpdate(*result.PlanUpdate)
		result.PlanUpdate = &planUpdate
	}
	return result
}

func cloneWebSearch(search model.WebSearch) model.WebSearch {
	search.Action.Queries = append([]string(nil), search.Action.Queries...)
	return search
}

func cloneImageGeneration(imageGeneration model.ImageGeneration) model.ImageGeneration {
	return imageGeneration
}

func clonePlanUpdate(update model.PlanUpdate) model.PlanUpdate {
	update.Plan = append([]model.PlanStep(nil), update.Plan...)
	return update
}

func cloneHookPrompt(prompt model.HookPrompt) model.HookPrompt {
	prompt.Fragments = append([]model.HookPromptFragment(nil), prompt.Fragments...)
	return prompt
}

func cloneItems(items []model.Item) []model.Item {
	cloned := make([]model.Item, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, cloneItem(item))
	}
	return cloned
}

func cloneItem(item model.Item) model.Item {
	item.Parts = cloneContentParts(item.Parts)
	if item.ToolCall != nil {
		toolCall := cloneToolCall(*item.ToolCall)
		item.ToolCall = &toolCall
	}
	if item.ToolResult != nil {
		toolResult := cloneToolResult(*item.ToolResult)
		item.ToolResult = &toolResult
	}
	if item.WebSearch != nil {
		webSearch := cloneWebSearch(*item.WebSearch)
		item.WebSearch = &webSearch
	}
	if item.HookPrompt != nil {
		hookPrompt := cloneHookPrompt(*item.HookPrompt)
		item.HookPrompt = &hookPrompt
	}
	if item.ImageGeneration != nil {
		imageGeneration := cloneImageGeneration(*item.ImageGeneration)
		item.ImageGeneration = &imageGeneration
	}
	item.MemoryCitation = model.CloneMemoryCitation(item.MemoryCitation)
	return item
}

func cloneContentParts(parts []model.ContentPart) []model.ContentPart {
	return append([]model.ContentPart(nil), parts...)
}
