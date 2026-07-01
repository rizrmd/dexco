package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openai/codex/dexco/internal/events"
	"github.com/openai/codex/dexco/internal/imageprep"
	"github.com/openai/codex/dexco/internal/model"
	"github.com/openai/codex/dexco/internal/runner"
)

type Config struct {
	Instructions string
	// WebSearch adapts Codex's request-side hosted web_search configuration.
	// Dexco does not serialize provider requests itself; instead, it resolves
	// the mode/config into Prompt.WebSearch for model adapters to encode.
	WebSearch *model.WebSearchRequest
	// ModelSwitchInstructions adapts Codex's `<model_switch>` developer
	// fragment. Codex derives the text from the next model's instructions when
	// a thread changes models; Dexco is provider-neutral, so callers provide the
	// already-rendered model guidance and the session preserves append-on-change
	// prompt history.
	ModelSwitchInstructions ModelSwitchInstructionsConfig
	// PermissionInstructions is Dexco's provider-neutral adaptation of Codex's
	// `<permissions instructions>` developer fragment. Codex derives this text
	// from permission profile, approval policy, exec policy, and enabled tools.
	// Dexco is a library, so callers provide the rendered bounded text and the
	// session handles Codex's "send once unless changed" history semantics.
	PermissionInstructions PermissionInstructionsConfig
	// CollaborationInstructions adapts Codex's CollaborationMode developer
	// fragment. Rust Codex derives this from ModeKind + Settings and wraps the
	// raw developer instructions in `<collaboration_mode>` tags; Dexco keeps the
	// model-visible contract while leaving mode selection to the embedding
	// library user.
	CollaborationInstructions CollaborationInstructionsConfig
	// StyleInstructions adapts Codex's `<personality_spec>` contextual
	// developer update. Codex's initial personality is folded into model
	// instructions by its model registry; Dexco callers should put any initial
	// style in Instructions and use this field or SetStyleInstructions for
	// later style changes that need Codex's append-only prompt-history behavior.
	StyleInstructions StyleInstructionsConfig
	// TimeReminder is Dexco's compact analogue of Codex's current-time reminder
	// prompt fragments. When configured, the session injects bounded developer
	// prompt text such as "It is ... UTC." before sampling; those fragments are
	// prompt metadata and are not committed as user/assistant history.
	TimeReminder TimeReminderConfig
	// ContextInstructions adapts Codex's AGENTS.md model context without
	// coupling Dexco to filesystem discovery. Callers provide an already-loaded
	// bounded snapshot; Dexco owns the append-only prompt-history semantics.
	ContextInstructions ContextInstructionsConfig
	// EnvironmentContext adapts Codex's `<environment_context>` world-state
	// fragment. Dexco callers provide environment facts explicitly; Dexco renders
	// the portable prompt shape without owning sandbox/network/filesystem policy.
	EnvironmentContext EnvironmentContextConfig
	// RolloutBudget adapts Codex's shared session token-budget reminders for a
	// single Dexco session. Dexco records completed response usage, injects
	// `<rollout_budget>` developer context when thresholds are crossed, and
	// rejects later sampling once exhausted.
	RolloutBudget RolloutBudgetConfig
}

type PermissionInstructionsConfig struct {
	Text     string
	Disabled bool
}

type ModelSwitchInstructionsConfig struct {
	// Text is the next model's instruction body. Dexco wraps it in Codex's
	// `<model_switch>` preamble before sending it as contextual developer text.
	Text     string
	Disabled bool
}

type CollaborationInstructionsConfig struct {
	// Text is the raw body Codex would store in
	// CollaborationMode.settings.developer_instructions. Dexco wraps it in the
	// same XML-like tags Codex uses before sending it to the model.
	Text     string
	Disabled bool
}

type StyleInstructionsConfig struct {
	// Text is the raw style/personality body. Dexco wraps it in Codex's
	// `<personality_spec>` preamble when emitting a contextual developer update.
	Text     string
	Disabled bool
}

type TimeReminderClock func(context.Context) (time.Time, error)

type TimeReminderConfig struct {
	Clock    TimeReminderClock
	Interval time.Duration
}

type ContextInstructionsConfig struct {
	Snapshot *ContextInstructionsSnapshot
	// MaxChars bounds Snapshot.Text before it becomes model-visible. When zero
	// or negative, Dexco uses a conservative default.
	MaxChars int
}

type ContextInstructionsSnapshot struct {
	Text    string
	Scope   string
	Sources []InstructionSource
}

type EnvironmentContextConfig struct {
	Snapshot *EnvironmentContextSnapshot
	MaxChars int
}

type EnvironmentContextSnapshot struct {
	Environments []EnvironmentState
	CurrentDate  string
	Timezone     string
	Subagents    string
}

type EnvironmentState struct {
	ID     string
	CWD    string
	Shell  string
	Status string
}

type RolloutBudgetConfig struct {
	LimitTokens               int64
	ReminderAtRemainingTokens []int64
	SamplingTokenWeight       float64
	PrefillTokenWeight        float64
}

var ErrRolloutBudgetExceeded = errors.New("rollout budget exceeded")

type InstructionSource struct {
	URI   string
	Label string
}

type UserShellCommandRecord struct {
	Command string
	// ExitCode is the completed process exit status supplied by the embedding
	// application. Dexco records it verbatim inside Codex's contextual user
	// fragment shape.
	ExitCode int
	Duration time.Duration
	Output   string
	// MaxOutputChars bounds Output before it enters model-visible history. When
	// zero, Dexco uses a conservative default; values below zero disable Dexco
	// truncation for callers that already applied their own hard cap.
	MaxOutputChars int
}

type SteerUserInputOptions struct {
	// ExpectedTurnID mirrors Codex's expected_turn_id guard for steered input.
	// Clients that receive a turn_started event can pass that ID back so stale UI
	// input is rejected instead of being queued onto a newer active turn.
	ExpectedTurnID string
}

type SubagentNotificationRecord struct {
	AgentPath string
	// Status should be a JSON-serializable Codex AgentStatus value. Unit states
	// can be strings such as "running"; terminal states can be tagged values
	// such as map[string]string{"completed": "final answer"}.
	Status any
}

type developerMessageKind string

const (
	developerMessageModelSwitch   developerMessageKind = "model_switch"
	developerMessagePermission    developerMessageKind = "permission"
	developerMessageCollaboration developerMessageKind = "collaboration"
	developerMessageStyle         developerMessageKind = "style"
	developerMessageTimeReminder  developerMessageKind = "time_reminder"
)

type developerMessageEntry struct {
	kind developerMessageKind
	text string
}

type Session struct {
	mu                            sync.Mutex
	nextID                        int
	history                       []model.Item
	activeContext                 map[string]model.AdditionalContextEntry
	activeTurn                    bool
	activeTurnID                  string
	pendingInput                  []model.Item
	pendingInputNotify            chan struct{}
	pendingInputSignaled          bool
	developerMessages             []developerMessageEntry
	lastModelSwitchMessage        string
	lastModelSwitchMessageValid   bool
	lastPermissionMessage         string
	lastPermissionMessageValid    bool
	lastCollaborationMessage      string
	lastCollaborationMessageValid bool
	lastStyleMessage              string
	lastStyleMessageValid         bool
	contextInstructions           *ContextInstructionsSnapshot
	contextInstructionsMaxChars   int
	environmentContext            *EnvironmentContextSnapshot
	environmentContextMaxChars    int
	rolloutBudget                 *rolloutBudgetState
	lastReminderAt                time.Time
	lastReminderAtValid           bool
	config                        Config
	runner                        *runner.Runner
}

func New(cfg Config, turnRunner *runner.Runner) (*Session, error) {
	if turnRunner == nil {
		return nil, fmt.Errorf("new session: nil runner")
	}

	session := &Session{
		nextID:                      1,
		config:                      cloneConfig(cfg),
		runner:                      turnRunner,
		contextInstructionsMaxChars: normalizeContextInstructionsLimit(cfg.ContextInstructions.MaxChars),
		environmentContextMaxChars:  normalizeEnvironmentContextLimit(cfg.EnvironmentContext.MaxChars),
		rolloutBudget:               newRolloutBudgetState(cfg.RolloutBudget),
	}
	if cfg.ContextInstructions.Snapshot != nil {
		snapshot := normalizeContextInstructionsSnapshot(
			*cfg.ContextInstructions.Snapshot,
			session.contextInstructionsMaxChars,
		)
		if snapshot.Text != "" {
			session.contextInstructions = &snapshot
			session.history = append(session.history, renderContextInstructionsItem(snapshot, contextInstructionsNoticeNone))
		}
	}
	if cfg.EnvironmentContext.Snapshot != nil {
		snapshot := normalizeEnvironmentContextSnapshot(
			*cfg.EnvironmentContext.Snapshot,
			session.environmentContextMaxChars,
		)
		if !snapshot.isEmpty() {
			session.environmentContext = &snapshot
			session.history = append(session.history, renderEnvironmentContextItem(snapshot))
		}
	}
	return session, nil
}

func cloneConfig(cfg Config) Config {
	cfg.WebSearch = model.CloneWebSearchRequest(cfg.WebSearch)
	return cfg
}

func (s *Session) webSearchForTurnLocked(override *model.WebSearchRequest) *model.WebSearchRequest {
	request := s.config.WebSearch
	if override != nil {
		request = override
	}
	return model.NormalizeWebSearchRequest(request)
}

func (s *Session) SubmitUserInput(
	ctx context.Context,
	op model.OpUserInput,
	sink events.Sink,
) (runner.TurnResult, error) {
	if sink == nil {
		sink = events.NopSink{}
	}

	s.mu.Lock()
	if s.activeTurn {
		s.mu.Unlock()
		return runner.TurnResult{}, fmt.Errorf("turn already running; use SteerUserInput to queue input for the active turn")
	}
	s.activeTurn = true
	s.pendingInput = nil
	s.pendingInputNotify = make(chan struct{})
	s.pendingInputSignaled = false
	if s.rolloutBudget != nil && s.rolloutBudget.exhausted {
		s.activeTurn = false
		s.pendingInputNotify = nil
		s.mu.Unlock()
		return runner.TurnResult{}, ErrRolloutBudgetExceeded
	}
	if item := s.rolloutBudgetReminderItemLocked(); item != nil {
		s.history = append(s.history, *item)
	}
	turnID := fmt.Sprintf("turn-%d", s.nextID)
	s.nextID++
	s.activeTurnID = turnID
	baseHistory := append([]model.Item(nil), s.history...)
	contextItems, nextActiveContext := s.additionalContextItemsLocked(op.AdditionalContext)
	webSearch := s.webSearchForTurnLocked(op.WebSearch)
	developerMessages, err := s.developerMessagesLocked(ctx)
	if err != nil {
		s.activeTurn = false
		s.activeTurnID = ""
		s.pendingInput = nil
		s.pendingInputNotify = nil
		s.pendingInputSignaled = false
		s.mu.Unlock()
		return runner.TurnResult{}, err
	}
	s.mu.Unlock()

	turn := model.Turn{
		ID:                turnID,
		History:           append(append(baseHistory, contextItems...), imageprep.UserInputItem(op.Input)),
		Instructions:      s.config.Instructions,
		DeveloperMessages: developerMessages,
		WebSearch:         webSearch,
		OutputSchema:      append([]byte(nil), op.OutputSchema...),
		Status:            model.TurnStatusRunning,
	}
	if err := sink.OnTurnStarted(ctx, turn); err != nil {
		s.finishActiveTurn()
		return runner.TurnResult{}, fmt.Errorf("turn started event: %w", err)
	}
	if clientSink, ok := sink.(events.ClientEventSink); ok {
		if err := clientSink.OnClientEvent(ctx, model.ClientEvent{
			Type:   model.ClientEventTurnStarted,
			TurnID: turn.ID,
			Turn:   &turn,
		}); err != nil {
			s.finishActiveTurn()
			return runner.TurnResult{}, fmt.Errorf("turn started client event: %w", err)
		}
	}

	result, err := s.runner.RunTurnWithOptions(ctx, turn, sink, runner.TurnOptions{
		PendingInput:         s.drainPendingInput,
		PendingInputActivity: s.pendingInputActivity,
	})
	if err != nil {
		if abortedItems, ok := runner.AbortedHistory(err); ok {
			s.mu.Lock()
			s.history = append(cloneItems(turn.History), abortedItems...)
			s.activeContext = nextActiveContext
			s.activeTurn = false
			s.activeTurnID = ""
			s.pendingInput = nil
			s.pendingInputNotify = nil
			s.pendingInputSignaled = false
			s.mu.Unlock()
			return runner.TurnResult{}, err
		}
		s.finishActiveTurn()
		return runner.TurnResult{}, err
	}

	s.mu.Lock()
	budgetErr := s.recordRolloutBudgetUsageLocked(result.TokenUsages)
	s.history = append([]model.Item(nil), result.History...)
	s.activeContext = nextActiveContext
	s.activeTurn = false
	s.activeTurnID = ""
	s.pendingInput = nil
	s.pendingInputNotify = nil
	s.pendingInputSignaled = false
	s.mu.Unlock()
	if budgetErr != nil {
		return result, budgetErr
	}

	return result, nil
}

func (s *Session) SetPermissionInstructions(text string) {
	s.mu.Lock()
	s.config.PermissionInstructions.Text = text
	s.config.PermissionInstructions.Disabled = false
	s.mu.Unlock()
}

func (s *Session) SetModelSwitchInstructions(text string) {
	s.mu.Lock()
	s.config.ModelSwitchInstructions.Text = text
	s.config.ModelSwitchInstructions.Disabled = false
	s.mu.Unlock()
}

func (s *Session) SetCollaborationInstructions(text string) {
	s.mu.Lock()
	s.config.CollaborationInstructions.Text = text
	s.config.CollaborationInstructions.Disabled = false
	s.mu.Unlock()
}

func (s *Session) SetStyleInstructions(text string) {
	s.mu.Lock()
	s.config.StyleInstructions.Text = text
	s.config.StyleInstructions.Disabled = false
	s.mu.Unlock()
}

func (s *Session) RecordUserShellCommand(record UserShellCommandRecord) error {
	if strings.TrimSpace(record.Command) == "" {
		return fmt.Errorf("record user shell command: empty command")
	}
	item := renderUserShellCommandItem(record)

	s.mu.Lock()
	defer s.mu.Unlock()
	// Codex treats user-initiated shell commands as auxiliary context: they do
	// not replace the active assistant turn, but they should become visible at
	// the next safe model continuation boundary.
	return s.appendContextualUserItemLocked(item)
}

func (s *Session) RecordSubagentNotification(record SubagentNotificationRecord) error {
	if strings.TrimSpace(record.AgentPath) == "" {
		return fmt.Errorf("record subagent notification: empty agent path")
	}
	if record.Status == nil {
		return fmt.Errorf("record subagent notification: nil status")
	}
	item, err := renderSubagentNotificationItem(record)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Codex delivers subagent completion/errors through contextual user
	// fragments. Dexco does not own multi-agent scheduling, but this preserves
	// the parent-loop invariant: notifications recorded during an active turn
	// wait until the next safe model continuation boundary.
	return s.appendContextualUserItemLocked(item)
}

func (s *Session) SetContextInstructions(snapshot ContextInstructionsSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	normalized := normalizeContextInstructionsSnapshot(snapshot, s.contextInstructionsMaxChars)
	if normalized.Text == "" {
		return s.clearContextInstructionsLocked()
	}

	if s.contextInstructions != nil &&
		s.contextInstructions.Scope == normalized.Scope &&
		s.contextInstructions.Text == normalized.Text {
		// Codex excludes source provenance from AGENTS.md world-state diffs.
		// Dexco still updates the caller-visible attribution list, but avoids
		// prompt churn when model-visible scope/text did not change.
		s.contextInstructions.Sources = cloneInstructionSources(normalized.Sources)
		return nil
	}

	notice := contextInstructionsNoticeNone
	if s.contextInstructions != nil {
		notice = contextInstructionsNoticeReplacement
	}
	item := renderContextInstructionsItem(normalized, notice)
	if err := s.appendContextualUserItemLocked(item); err != nil {
		return err
	}
	s.contextInstructions = &normalized
	return nil
}

func (s *Session) ClearContextInstructions() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clearContextInstructionsLocked()
}

func (s *Session) ContextInstructionSources() []InstructionSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contextInstructions == nil {
		return nil
	}
	return cloneInstructionSources(s.contextInstructions.Sources)
}

func (s *Session) SetEnvironmentContext(snapshot EnvironmentContextSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	normalized := normalizeEnvironmentContextSnapshot(snapshot, s.environmentContextMaxChars)
	if normalized.isEmpty() {
		return s.clearEnvironmentContextLocked()
	}
	if s.environmentContext != nil && environmentContextEqual(*s.environmentContext, normalized) {
		// Keep Dexco's cached world-state equivalent fresh even when no model
		// fragment is appended. This mirrors Codex's world-state diff behavior:
		// a suppressed no-op diff should still advance the baseline used for
		// later diffs, otherwise a later real shell change could be swallowed by
		// repeatedly comparing against the older "unknown shell" snapshot.
		s.environmentContext = &normalized
		return nil
	}
	item := renderEnvironmentContextItem(normalized)
	if err := s.appendContextualUserItemLocked(item); err != nil {
		return err
	}
	s.environmentContext = &normalized
	return nil
}

func (s *Session) ClearEnvironmentContext() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clearEnvironmentContextLocked()
}

func (s *Session) clearContextInstructionsLocked() error {
	if s.contextInstructions == nil {
		return nil
	}
	if err := s.appendContextualUserItemLocked(renderContextInstructionsRemovalItem()); err != nil {
		return err
	}
	s.contextInstructions = nil
	return nil
}

func (s *Session) clearEnvironmentContextLocked() error {
	s.environmentContext = nil
	return nil
}

func (s *Session) appendContextualUserItemLocked(item model.Item) error {
	if s.activeTurn {
		if len(s.pendingInput) >= maxPendingInputItems {
			return fmt.Errorf("append contextual user item: pending input queue full")
		}
		s.pendingInput = append(s.pendingInput, item)
		if !s.pendingInputSignaled && s.pendingInputNotify != nil {
			close(s.pendingInputNotify)
			s.pendingInputSignaled = true
		}
		return nil
	}
	s.history = append(s.history, item)
	return nil
}

const maxPendingInputItems = 16

func (s *Session) SteerUserInput(ctx context.Context, input model.UserInput) error {
	_, err := s.SteerUserInputWithOptions(ctx, input, SteerUserInputOptions{})
	return err
}

func (s *Session) SteerUserInputWithOptions(
	ctx context.Context,
	input model.UserInput,
	options SteerUserInputOptions,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	s.mu.Lock()
	active := s.activeTurn
	activeTurnID := s.activeTurnID
	if active && options.ExpectedTurnID != "" && options.ExpectedTurnID != activeTurnID {
		s.mu.Unlock()
		return "", fmt.Errorf(
			"steer user input: expected active turn %q, got %q",
			options.ExpectedTurnID,
			activeTurnID,
		)
	}
	if active && len(s.pendingInput) >= maxPendingInputItems {
		s.mu.Unlock()
		return "", fmt.Errorf("steer user input: pending input queue full")
	}
	s.mu.Unlock()
	if !active {
		return "", fmt.Errorf("steer user input: no active turn")
	}

	item := imageprep.UserInputItem(input)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.activeTurn {
		return "", fmt.Errorf("steer user input: no active turn")
	}
	activeTurnID = s.activeTurnID
	if options.ExpectedTurnID != "" && options.ExpectedTurnID != activeTurnID {
		return "", fmt.Errorf(
			"steer user input: expected active turn %q, got %q",
			options.ExpectedTurnID,
			activeTurnID,
		)
	}
	if len(s.pendingInput) >= maxPendingInputItems {
		return "", fmt.Errorf("steer user input: pending input queue full")
	}
	// Codex pending_input parity: steer input is queued separately from durable
	// history and becomes visible only when the runner reaches a safe
	// continuation point. It is not committed if the active turn fails before
	// that point.
	s.pendingInput = append(s.pendingInput, item)
	if !s.pendingInputSignaled && s.pendingInputNotify != nil {
		close(s.pendingInputNotify)
		s.pendingInputSignaled = true
	}
	return activeTurnID, nil
}

func (s *Session) drainPendingInput(context.Context) ([]model.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pendingInput) == 0 {
		return nil, nil
	}
	items := cloneItems(s.pendingInput)
	s.pendingInput = nil
	if s.activeTurn {
		s.pendingInputNotify = make(chan struct{})
		s.pendingInputSignaled = false
	}
	return items, nil
}

func (s *Session) pendingInputActivity(context.Context) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingInputNotify
}

func (s *Session) finishActiveTurn() {
	s.mu.Lock()
	s.activeTurn = false
	s.activeTurnID = ""
	s.pendingInput = nil
	s.pendingInputNotify = nil
	s.pendingInputSignaled = false
	s.mu.Unlock()
}

type rolloutBudgetState struct {
	limitTokens               int64
	reminderAtRemainingTokens []int64
	samplingTokenWeight       float64
	prefillTokenWeight        float64
	weightedTokensUsed        float64
	deliveredReminderIndex    int
	reminderDelivered         bool
	exhausted                 bool
}

func newRolloutBudgetState(config RolloutBudgetConfig) *rolloutBudgetState {
	if config.LimitTokens <= 0 {
		return nil
	}
	samplingWeight := config.SamplingTokenWeight
	if samplingWeight == 0 {
		samplingWeight = 1
	}
	prefillWeight := config.PrefillTokenWeight
	if prefillWeight == 0 {
		prefillWeight = 1
	}
	thresholds := make([]int64, 0, len(config.ReminderAtRemainingTokens))
	for _, threshold := range config.ReminderAtRemainingTokens {
		if threshold > 0 && threshold < config.LimitTokens {
			thresholds = append(thresholds, threshold)
		}
	}
	return &rolloutBudgetState{
		limitTokens:               config.LimitTokens,
		reminderAtRemainingTokens: thresholds,
		samplingTokenWeight:       samplingWeight,
		prefillTokenWeight:        prefillWeight,
	}
}

func (s *Session) rolloutBudgetReminderItemLocked() *model.Item {
	if s.rolloutBudget == nil {
		return nil
	}
	remaining := s.rolloutBudget.remainingTokens()
	reminderIndex := s.rolloutBudget.reminderIndex(remaining)
	if s.rolloutBudget.reminderDelivered &&
		s.rolloutBudget.deliveredReminderIndex >= reminderIndex {
		return nil
	}
	s.rolloutBudget.reminderDelivered = true
	s.rolloutBudget.deliveredReminderIndex = reminderIndex
	item := renderRolloutBudgetItem(remaining)
	return &item
}

func (s *Session) recordRolloutBudgetUsageLocked(usages []model.TokenUsage) error {
	if s.rolloutBudget == nil {
		return nil
	}
	for _, usage := range usages {
		s.rolloutBudget.recordUsage(usage)
	}
	if s.rolloutBudget.exhausted {
		return ErrRolloutBudgetExceeded
	}
	return nil
}

func (b *rolloutBudgetState) remainingTokens() int64 {
	remaining := float64(b.limitTokens) - b.weightedTokensUsed
	if remaining < 0 {
		return 0
	}
	return int64(math.Floor(remaining))
}

func (b *rolloutBudgetState) reminderIndex(remainingTokens int64) int {
	count := 0
	for _, threshold := range b.reminderAtRemainingTokens {
		if remainingTokens <= threshold {
			count++
		}
	}
	return count
}

func (b *rolloutBudgetState) recordUsage(usage model.TokenUsage) {
	outputTokens := maxInt64(usage.OutputTokens, 0)
	nonCachedInputTokens := maxInt64(usage.InputTokens-usage.CachedInputTokens, 0)
	b.weightedTokensUsed += float64(outputTokens)*b.samplingTokenWeight +
		float64(nonCachedInputTokens)*b.prefillTokenWeight
	if b.weightedTokensUsed >= float64(b.limitTokens) {
		b.exhausted = true
	}
}

func renderRolloutBudgetItem(remainingTokens int64) model.Item {
	return model.ContextItem(
		"developer",
		"rollout_budget",
		fmt.Sprintf(
			"<rollout_budget>\nYou have %d weighted tokens left in the shared session token budget.\n</rollout_budget>",
			remainingTokens,
		),
	)
}

func maxInt64(value int64, floor int64) int64 {
	if value < floor {
		return floor
	}
	return value
}

const environmentContextDefaultLimit = 8192

func normalizeEnvironmentContextLimit(maxChars int) int {
	if maxChars <= 0 {
		return environmentContextDefaultLimit
	}
	return maxChars
}

func normalizeEnvironmentContextSnapshot(
	snapshot EnvironmentContextSnapshot,
	maxChars int,
) EnvironmentContextSnapshot {
	environments := make([]EnvironmentState, 0, len(snapshot.Environments))
	for _, environment := range snapshot.Environments {
		if strings.TrimSpace(environment.ID) == "" &&
			strings.TrimSpace(environment.CWD) == "" &&
			strings.TrimSpace(environment.Shell) == "" &&
			strings.TrimSpace(environment.Status) == "" {
			continue
		}
		environments = append(environments, EnvironmentState{
			ID:     strings.TrimSpace(environment.ID),
			CWD:    environment.CWD,
			Shell:  environment.Shell,
			Status: strings.TrimSpace(environment.Status),
		})
	}
	sort.Slice(environments, func(i, j int) bool {
		return environments[i].ID < environments[j].ID
	})
	normalized := EnvironmentContextSnapshot{
		Environments: environments,
		CurrentDate:  strings.TrimSpace(snapshot.CurrentDate),
		Timezone:     strings.TrimSpace(snapshot.Timezone),
		Subagents:    strings.TrimSpace(snapshot.Subagents),
	}
	return truncateEnvironmentContextSnapshot(normalized, maxChars)
}

func (s EnvironmentContextSnapshot) isEmpty() bool {
	return len(s.Environments) == 0 &&
		s.CurrentDate == "" &&
		s.Timezone == "" &&
		s.Subagents == ""
}

func environmentContextEqual(a EnvironmentContextSnapshot, b EnvironmentContextSnapshot) bool {
	if a.CurrentDate != b.CurrentDate || a.Timezone != b.Timezone || a.Subagents != b.Subagents {
		return false
	}
	if len(a.Environments) != len(b.Environments) {
		return false
	}
	for i := range a.Environments {
		if a.Environments[i] == b.Environments[i] {
			continue
		}
		if len(a.Environments) == 1 &&
			len(b.Environments) == 1 &&
			a.Environments[i].ID == b.Environments[i].ID &&
			a.Environments[i].CWD == b.Environments[i].CWD &&
			a.Environments[i].Status == b.Environments[i].Status &&
			a.Environments[i].Shell == "" &&
			b.Environments[i].Shell != "" {
			// Codex treats the legacy single-environment "shell was unknown,
			// shell is now known" diff as no-op. The old render shape did not
			// expose an unknown shell marker, so adding a fresh context item only
			// because discovery filled in zsh/bash would churn model context and
			// prompt-cache keys without changing the user's effective workspace.
			continue
		}
		return false
	}
	return true
}

func truncateEnvironmentContextSnapshot(
	snapshot EnvironmentContextSnapshot,
	maxChars int,
) EnvironmentContextSnapshot {
	if len([]rune(renderEnvironmentContext(snapshot))) <= maxChars {
		return snapshot
	}
	snapshot.Subagents = truncateBoundedText(snapshot.Subagents, maxChars/4, "environment context")
	for i := range snapshot.Environments {
		snapshot.Environments[i].CWD = truncateBoundedText(snapshot.Environments[i].CWD, maxChars/4, "environment context")
		snapshot.Environments[i].Shell = truncateBoundedText(snapshot.Environments[i].Shell, maxChars/8, "environment context")
	}
	return snapshot
}

func truncateBoundedText(text string, maxChars int, label string) string {
	if maxChars <= 0 {
		maxChars = 32
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	if maxChars < 2 {
		return string(runes[:maxChars])
	}
	headChars := maxChars / 2
	tailChars := maxChars - headChars
	omitted := len(runes) - headChars - tailChars
	return fmt.Sprintf(
		"%s... %d %s characters truncated ...%s",
		string(runes[:headChars]),
		omitted,
		label,
		string(runes[len(runes)-tailChars:]),
	)
}

func renderEnvironmentContextItem(snapshot EnvironmentContextSnapshot) model.Item {
	return model.ContextItem("user", "environment_context", renderEnvironmentContext(snapshot))
}

func renderEnvironmentContext(snapshot EnvironmentContextSnapshot) string {
	var builder strings.Builder
	builder.WriteString("<environment_context>\n")
	if len(snapshot.Environments) == 1 && snapshot.Environments[0].Status == "" {
		renderEnvironmentValues(&builder, snapshot.Environments[0], "  ")
	} else if len(snapshot.Environments) > 0 {
		builder.WriteString("  <environments>\n")
		for _, environment := range snapshot.Environments {
			if environment.Status == "unavailable" {
				builder.WriteString("    <environment id=\"")
				builder.WriteString(xmlText(environment.ID))
				builder.WriteString("\" status=\"unavailable\" />\n")
				continue
			}
			builder.WriteString("    <environment id=\"")
			builder.WriteString(xmlText(environment.ID))
			builder.WriteString("\">\n")
			renderEnvironmentValues(&builder, environment, "      ")
			builder.WriteString("    </environment>\n")
		}
		builder.WriteString("  </environments>\n")
	}
	renderOptionalEnvironmentElement(&builder, "current_date", snapshot.CurrentDate)
	renderOptionalEnvironmentElement(&builder, "timezone", snapshot.Timezone)
	if snapshot.Subagents != "" {
		builder.WriteString("  <subagents>\n")
		for _, line := range strings.Split(snapshot.Subagents, "\n") {
			builder.WriteString("    ")
			builder.WriteString(xmlText(line))
			builder.WriteByte('\n')
		}
		builder.WriteString("  </subagents>\n")
	}
	builder.WriteString("</environment_context>")
	return builder.String()
}

func renderEnvironmentValues(builder *strings.Builder, environment EnvironmentState, indent string) {
	if environment.CWD != "" {
		builder.WriteString(indent)
		builder.WriteString("<cwd>")
		builder.WriteString(xmlText(environment.CWD))
		builder.WriteString("</cwd>\n")
	}
	if environment.Status == "starting" {
		builder.WriteString(indent)
		builder.WriteString("<status>starting</status>\n")
	}
	if environment.Shell != "" {
		builder.WriteString(indent)
		builder.WriteString("<shell>")
		builder.WriteString(xmlText(environment.Shell))
		builder.WriteString("</shell>\n")
	}
}

func renderOptionalEnvironmentElement(builder *strings.Builder, name string, value string) {
	if value == "" {
		return
	}
	builder.WriteString("  <")
	builder.WriteString(name)
	builder.WriteString(">")
	builder.WriteString(xmlText(value))
	builder.WriteString("</")
	builder.WriteString(name)
	builder.WriteString(">\n")
}

func xmlText(text string) string {
	return html.EscapeString(text)
}

func cloneItems(items []model.Item) []model.Item {
	cloned := make([]model.Item, 0, len(items))
	for _, item := range items {
		item.Parts = append([]model.ContentPart(nil), item.Parts...)
		if item.ToolCall != nil {
			toolCall := *item.ToolCall
			toolCall.Arguments = append([]byte(nil), toolCall.Arguments...)
			item.ToolCall = &toolCall
		}
		if item.ToolResult != nil {
			toolResult := *item.ToolResult
			toolResult.Parts = append([]model.ContentPart(nil), toolResult.Parts...)
			item.ToolResult = &toolResult
		}
		if item.WebSearch != nil {
			webSearch := *item.WebSearch
			webSearch.Action.Queries = append([]string(nil), webSearch.Action.Queries...)
			item.WebSearch = &webSearch
		}
		if item.HookPrompt != nil {
			hookPrompt := *item.HookPrompt
			hookPrompt.Fragments = append([]model.HookPromptFragment(nil), hookPrompt.Fragments...)
			item.HookPrompt = &hookPrompt
		}
		if item.ImageGeneration != nil {
			imageGeneration := *item.ImageGeneration
			item.ImageGeneration = &imageGeneration
		}
		cloned = append(cloned, item)
	}
	return cloned
}

const (
	additionalContextValueLimit = 4096
	additionalContextEdge       = 1024
)

func (s *Session) additionalContextItemsLocked(
	context map[string]model.AdditionalContextEntry,
) ([]model.Item, map[string]model.AdditionalContextEntry) {
	if len(context) == 0 {
		return nil, map[string]model.AdditionalContextEntry{}
	}

	keys := make([]string, 0, len(context))
	for key := range context {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	items := make([]model.Item, 0, len(keys))
	nextActive := make(map[string]model.AdditionalContextEntry, len(context))
	for _, key := range keys {
		entry := context[key]
		nextActive[key] = entry
		if previous, ok := s.activeContext[key]; ok && previous == entry {
			continue
		}
		items = append(items, renderAdditionalContextItem(key, entry))
	}
	return items, nextActive
}

func renderAdditionalContextItem(key string, entry model.AdditionalContextEntry) model.Item {
	value := truncateAdditionalContextValue(entry.Value)
	if entry.Kind == model.AdditionalContextApplication {
		return model.ContextItem("developer", key, fmt.Sprintf("<%s>%s</%s>", key, value, key))
	}
	// Codex treats untrusted app context as user-role text wrapped in an
	// `external_` tag. Dexco preserves that trust boundary so application state
	// cannot silently gain developer-message authority.
	tag := "external_" + key
	return model.ContextItem("user", key, fmt.Sprintf("<%s>%s</%s>", tag, value, tag))
}

type contextInstructionsNotice int

const (
	contextInstructionsNoticeNone contextInstructionsNotice = iota
	contextInstructionsNoticeReplacement
)

const (
	contextInstructionsDefaultLimit = 8192
	contextInstructionsContextKey   = "agents_md"
	contextInstructionsReplaceText  = "These AGENTS.md instructions replace all previously provided AGENTS.md instructions."
	contextInstructionsRemovalText  = "The previously provided AGENTS.md instructions no longer apply."
)

func normalizeContextInstructionsLimit(maxChars int) int {
	if maxChars <= 0 {
		return contextInstructionsDefaultLimit
	}
	return maxChars
}

func normalizeContextInstructionsSnapshot(
	snapshot ContextInstructionsSnapshot,
	maxChars int,
) ContextInstructionsSnapshot {
	text := truncateContextInstructionsText(snapshot.Text, maxChars)
	return ContextInstructionsSnapshot{
		Text:    text,
		Scope:   snapshot.Scope,
		Sources: cloneInstructionSources(snapshot.Sources),
	}
}

func renderContextInstructionsItem(
	snapshot ContextInstructionsSnapshot,
	notice contextInstructionsNotice,
) model.Item {
	text := snapshot.Text
	if notice == contextInstructionsNoticeReplacement {
		text = contextInstructionsReplaceText + "\n\n" + text
	}
	return model.ContextItem("user", contextInstructionsContextKey, renderContextInstructionsText(snapshot.Scope, text))
}

func renderContextInstructionsRemovalItem() model.Item {
	return model.ContextItem(
		"user",
		contextInstructionsContextKey,
		renderContextInstructionsText("", contextInstructionsRemovalText),
	)
}

func renderContextInstructionsText(scope string, text string) string {
	scopeSuffix := ""
	if scope != "" {
		scopeSuffix = " for " + scope
	}
	return fmt.Sprintf(
		"# AGENTS.md instructions%s\n\n<INSTRUCTIONS>\n%s\n</INSTRUCTIONS>",
		scopeSuffix,
		text,
	)
}

func truncateContextInstructionsText(text string, maxChars int) string {
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	if maxChars < 2 {
		return fmt.Sprintf("%s\n... AGENTS.md instructions truncated ...", string(runes[:maxChars]))
	}
	headChars := maxChars / 2
	tailChars := maxChars - headChars
	omitted := len(runes) - headChars - tailChars
	return fmt.Sprintf(
		"%s\n... %d AGENTS.md instruction characters truncated ...\n%s",
		string(runes[:headChars]),
		omitted,
		string(runes[len(runes)-tailChars:]),
	)
}

func cloneInstructionSources(sources []InstructionSource) []InstructionSource {
	return append([]InstructionSource(nil), sources...)
}

func truncateAdditionalContextValue(value string) string {
	runes := []rune(value)
	if len(runes) <= additionalContextValueLimit {
		return value
	}
	head := string(runes[:additionalContextEdge])
	tail := string(runes[len(runes)-additionalContextEdge:])
	omitted := len(runes) - additionalContextEdge - additionalContextEdge
	estimatedTokens := max(1, omitted/4)
	return fmt.Sprintf("%s\n... %d tokens truncated ...\n%s", head, estimatedTokens, tail)
}

const (
	userShellCommandContextKey     = "user_shell_command"
	userShellOutputDefaultLimit    = 4096
	userShellOutputTruncationToken = "characters truncated"
)

func renderUserShellCommandItem(record UserShellCommandRecord) model.Item {
	output := truncateUserShellOutput(record.Output, record.MaxOutputChars)
	content := fmt.Sprintf(
		"<user_shell_command>\n<command>\n%s\n</command>\n<result>\nExit code: %d\nDuration: %.4f seconds\nOutput:\n%s\n</result>\n</user_shell_command>",
		record.Command,
		record.ExitCode,
		record.Duration.Seconds(),
		output,
	)
	return model.ContextItem("user", userShellCommandContextKey, content)
}

const (
	subagentNotificationContextKey              = "subagent_notification"
	subagentNotificationContextMaxChars         = 4096
	subagentNotificationAgentPathMaxChars       = 512
	subagentNotificationStatusPreviewMaxChars   = 2048
	subagentNotificationStatusPreviewMinChars   = 128
	subagentNotificationStatusTruncationMessage = "subagent notification status"
)

func renderSubagentNotificationItem(record SubagentNotificationRecord) (model.Item, error) {
	payload := struct {
		AgentPath string `json:"agent_path"`
		Status    any    `json:"status"`
	}{
		AgentPath: truncateBoundedText(
			record.AgentPath,
			subagentNotificationAgentPathMaxChars,
			"subagent notification agent_path",
		),
		Status: record.Status,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return model.Item{}, fmt.Errorf("render subagent notification: %w", err)
	}
	content := fmt.Sprintf(
		"<subagent_notification>\n%s\n</subagent_notification>",
		string(encoded),
	)
	if len([]rune(content)) > subagentNotificationContextMaxChars {
		statusJSON, err := json.Marshal(record.Status)
		if err != nil {
			return model.Item{}, fmt.Errorf("render subagent notification status: %w", err)
		}
		// Codex bounds inter-agent completion/error text before it becomes
		// parent-visible context. Dexco accepts arbitrary JSON-serializable
		// status values from embedders, so oversized payloads are replaced with
		// a JSON-valid preview instead of truncating the full wrapper into
		// malformed JSON. Future Codex changes to completion-message limits
		// should be mirrored here at this single adaptation point.
		for previewChars := subagentNotificationStatusPreviewMaxChars; previewChars >= subagentNotificationStatusPreviewMinChars; previewChars /= 2 {
			payload.Status = struct {
				Truncated              bool   `json:"truncated"`
				OriginalCharacterCount int    `json:"original_character_count"`
				Preview                string `json:"preview"`
			}{
				Truncated:              true,
				OriginalCharacterCount: len([]rune(string(statusJSON))),
				Preview: truncateBoundedText(
					string(statusJSON),
					previewChars,
					subagentNotificationStatusTruncationMessage,
				),
			}
			encoded, err = json.Marshal(payload)
			if err != nil {
				return model.Item{}, fmt.Errorf("render truncated subagent notification: %w", err)
			}
			content = fmt.Sprintf(
				"<subagent_notification>\n%s\n</subagent_notification>",
				string(encoded),
			)
			if len([]rune(content)) <= subagentNotificationContextMaxChars {
				break
			}
		}
	}
	return model.ContextItem("user", subagentNotificationContextKey, content), nil
}

func truncateUserShellOutput(output string, maxChars int) string {
	if maxChars < 0 || alreadyTruncatedUserShellOutput(output) {
		return output
	}
	if maxChars == 0 {
		maxChars = userShellOutputDefaultLimit
	}
	runes := []rune(output)
	if len(runes) <= maxChars {
		return output
	}
	if maxChars < 2 {
		return fmt.Sprintf(
			"Warning: truncated output (original character count: %d)\n\n%s",
			len(runes),
			string(runes[:maxChars]),
		)
	}
	headChars := maxChars / 2
	tailChars := maxChars - headChars
	omitted := len(runes) - headChars - tailChars
	return fmt.Sprintf(
		"Warning: truncated output (original character count: %d)\n\n%s\n... %d %s ...\n%s",
		len(runes),
		string(runes[:headChars]),
		omitted,
		userShellOutputTruncationToken,
		string(runes[len(runes)-tailChars:]),
	)
}

func alreadyTruncatedUserShellOutput(output string) bool {
	return strings.Contains(output, "Warning: truncated output") &&
		strings.Contains(output, userShellOutputTruncationToken)
}

func (s *Session) developerMessagesLocked(ctx context.Context) ([]string, error) {
	modelSwitchAppended := s.appendModelSwitchInstructionsLocked()
	s.appendPermissionInstructionsLocked()
	s.appendCollaborationInstructionsLocked()
	s.appendStyleInstructionsLocked(modelSwitchAppended)

	config := s.config.TimeReminder
	if config.Clock == nil {
		return s.visibleDeveloperMessagesLocked(), nil
	}
	now, err := config.Clock(ctx)
	if err != nil {
		// Codex stops before inference when it cannot read the configured clock.
		// Dexco returns the error directly and avoids emitting a turn-start event
		// or calling the model.
		return nil, fmt.Errorf("read current time reminder: %w", err)
	}
	if s.shouldAppendReminderLocked(now, config.Interval) {
		s.appendDeveloperMessageLocked(developerMessageTimeReminder, formatCurrentTimeReminder(now))
		s.lastReminderAt = now
		s.lastReminderAtValid = true
	}
	return s.visibleDeveloperMessagesLocked(), nil
}

const permissionInstructionsLimit = 4096

const (
	modelSwitchInstructionsOpenTag          = "<model_switch>"
	modelSwitchInstructionsCloseTag         = "</model_switch>"
	modelSwitchInstructionsBodyLimit        = 4096
	modelSwitchInstructionsTruncatedMessage = "\n... model switch instructions truncated ..."
)

func (s *Session) appendModelSwitchInstructionsLocked() bool {
	config := s.config.ModelSwitchInstructions
	if config.Disabled || config.Text == "" {
		return false
	}
	message := renderModelSwitchInstructions(config.Text)
	if s.lastModelSwitchMessageValid && s.lastModelSwitchMessage == message {
		return false
	}
	// Codex prepends model-switch guidance before other settings updates so the
	// model sees the new model's instructions before interpreting any remaining
	// contextual fragments. Dexco preserves that ordering at the library prompt
	// boundary while leaving actual provider/model routing to callers.
	s.appendDeveloperMessageLocked(developerMessageModelSwitch, message)
	s.lastModelSwitchMessage = message
	s.lastModelSwitchMessageValid = true
	return true
}

func renderModelSwitchInstructions(text string) string {
	return fmt.Sprintf(
		"%s\nThe user was previously using a different model. Please continue the conversation according to the following instructions:\n\n%s\n%s",
		modelSwitchInstructionsOpenTag,
		truncateModelSwitchInstructions(text),
		modelSwitchInstructionsCloseTag,
	)
}

func truncateModelSwitchInstructions(text string) string {
	runes := []rune(text)
	if len(runes) <= modelSwitchInstructionsBodyLimit {
		return text
	}
	head := string(runes[:modelSwitchInstructionsBodyLimit])
	return head + modelSwitchInstructionsTruncatedMessage
}

func (s *Session) appendPermissionInstructionsLocked() {
	config := s.config.PermissionInstructions
	if config.Disabled || config.Text == "" {
		return
	}
	text := truncatePermissionInstructions(config.Text)
	if !s.lastPermissionMessageValid || s.lastPermissionMessage != text {
		// Codex appends a fresh permissions developer fragment when the effective
		// policy changes, but reuses the existing fragment on ordinary turns so
		// prompt context is stable and cache-friendly.
		s.appendDeveloperMessageLocked(developerMessagePermission, text)
		s.lastPermissionMessage = text
		s.lastPermissionMessageValid = true
	}
}

func truncatePermissionInstructions(text string) string {
	runes := []rune(text)
	if len(runes) <= permissionInstructionsLimit {
		return text
	}
	head := string(runes[:permissionInstructionsLimit])
	return fmt.Sprintf("%s\n... permission instructions truncated ...", head)
}

const (
	collaborationModeOpenTag              = "<collaboration_mode>"
	collaborationModeCloseTag             = "</collaboration_mode>"
	collaborationInstructionsBodyLimit    = 4096
	collaborationInstructionsRetainedText = "\n... collaboration instructions truncated ..."
)

func (s *Session) appendCollaborationInstructionsLocked() {
	config := s.config.CollaborationInstructions
	if config.Disabled {
		return
	}
	if config.Text == "" {
		// Codex ignores an empty CollaborationMode developer_instructions update:
		// no new fragment is emitted, so previously model-visible collaboration
		// guidance remains in the prompt history. Dexco mirrors that behavior;
		// callers that want to suppress all collaboration text should set Disabled.
		return
	}

	message := renderCollaborationInstructions(config.Text)
	if !s.lastCollaborationMessageValid || s.lastCollaborationMessage != message {
		// Rust Codex appends a new contextual developer fragment when the
		// effective collaboration mode changes and avoids appending on no-op
		// updates. Dexco lacks Codex's full ModeKind object, so the rendered
		// bounded text is the effective key for this provider-neutral library API.
		s.appendDeveloperMessageLocked(developerMessageCollaboration, message)
		s.lastCollaborationMessage = message
		s.lastCollaborationMessageValid = true
	}
}

func renderCollaborationInstructions(text string) string {
	return fmt.Sprintf(
		"%s%s%s",
		collaborationModeOpenTag,
		truncateCollaborationInstructions(text),
		collaborationModeCloseTag,
	)
}

func truncateCollaborationInstructions(text string) string {
	runes := []rune(text)
	if len(runes) <= collaborationInstructionsBodyLimit {
		return text
	}
	head := string(runes[:collaborationInstructionsBodyLimit])
	return head + collaborationInstructionsRetainedText
}

const (
	styleInstructionsOpenTag          = "<personality_spec>"
	styleInstructionsCloseTag         = "</personality_spec>"
	styleInstructionsBodyLimit        = 4096
	styleInstructionsTruncatedMessage = "\n... personality instructions truncated ..."
)

func (s *Session) appendStyleInstructionsLocked(modelSwitchAppended bool) {
	config := s.config.StyleInstructions
	if config.Disabled || config.Text == "" {
		// Codex emits no personality update for explicit "none" or empty style
		// text. Existing style fragments stay in model-visible history because
		// they are contextual history items, not mutable session state.
		return
	}

	message := renderStyleInstructions(config.Text)
	if modelSwitchAppended {
		// Codex suppresses `<personality_spec>` when the model changes in the
		// same turn because the `<model_switch>` instructions are built from the
		// new model/personality combination. Treat the current style text as the
		// new baseline so a later no-op style set does not append stale guidance.
		s.lastStyleMessage = message
		s.lastStyleMessageValid = true
		return
	}
	if !s.lastStyleMessageValid || s.lastStyleMessage != message {
		// Codex's personality updates are cache-friendly contextual developer
		// fragments: append a new `<personality_spec>` only when the effective
		// style text changes and never rewrite prior prompt history.
		s.appendDeveloperMessageLocked(developerMessageStyle, message)
		s.lastStyleMessage = message
		s.lastStyleMessageValid = true
	}
}

func renderStyleInstructions(text string) string {
	return fmt.Sprintf(
		"%s The user has requested a new communication style. Future messages should adhere to the following personality: \n%s %s",
		styleInstructionsOpenTag,
		truncateStyleInstructions(text),
		styleInstructionsCloseTag,
	)
}

func truncateStyleInstructions(text string) string {
	runes := []rune(text)
	if len(runes) <= styleInstructionsBodyLimit {
		return text
	}
	head := string(runes[:styleInstructionsBodyLimit])
	return head + styleInstructionsTruncatedMessage
}

func (s *Session) appendDeveloperMessageLocked(kind developerMessageKind, text string) {
	// Codex stores contextual developer fragments as ordinary model-visible
	// response items, so mixed updates keep the order in which they became
	// visible to the model. Dexco exposes them as Prompt.DeveloperMessages but
	// preserves that chronological log instead of regrouping by fragment type.
	s.developerMessages = append(s.developerMessages, developerMessageEntry{
		kind: kind,
		text: text,
	})
}

func (s *Session) visibleDeveloperMessagesLocked() []string {
	messages := make([]string, 0, len(s.developerMessages))
	for _, entry := range s.developerMessages {
		if !s.shouldShowDeveloperMessageLocked(entry.kind) {
			continue
		}
		messages = append(messages, entry.text)
	}
	return messages
}

func (s *Session) shouldShowDeveloperMessageLocked(kind developerMessageKind) bool {
	switch kind {
	case developerMessageModelSwitch:
		return !s.config.ModelSwitchInstructions.Disabled && s.config.ModelSwitchInstructions.Text != ""
	case developerMessagePermission:
		return !s.config.PermissionInstructions.Disabled && s.config.PermissionInstructions.Text != ""
	case developerMessageCollaboration:
		return !s.config.CollaborationInstructions.Disabled
	case developerMessageStyle:
		return !s.config.StyleInstructions.Disabled
	case developerMessageTimeReminder:
		return true
	}
	return true
}

func (s *Session) shouldAppendReminderLocked(now time.Time, interval time.Duration) bool {
	if !s.lastReminderAtValid {
		return true
	}
	if interval <= 0 {
		return true
	}
	return !now.Before(s.lastReminderAt) && now.Sub(s.lastReminderAt) >= interval
}

func formatCurrentTimeReminder(now time.Time) string {
	return fmt.Sprintf("It is %s UTC.", now.UTC().Format("2006-01-02 15:04:05"))
}
