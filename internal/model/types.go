package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ItemKind string

const (
	ItemContext          ItemKind = "context"
	ItemUserInput        ItemKind = "user_input"
	ItemAssistantMessage ItemKind = "assistant_message"
	ItemReasoning        ItemKind = "reasoning"
	ItemToolCall         ItemKind = "tool_call"
	ItemToolResult       ItemKind = "tool_result"
	ItemWebSearch        ItemKind = "web_search"
	ItemHookPrompt       ItemKind = "hook_prompt"
	ItemImageGeneration  ItemKind = "image_generation"
)

const TurnAbortedGuidance = "The previous turn was interrupted on purpose. Any running tools may have partially executed."

type Item struct {
	Kind            ItemKind
	Role            string
	Content         string
	ContextKey      string
	Parts           []ContentPart
	ToolCall        *ToolCall
	ToolResult      *ToolResult
	WebSearch       *WebSearch
	HookPrompt      *HookPrompt
	ImageGeneration *ImageGeneration
	// MemoryCitation mirrors Codex assistant-message citation metadata. The
	// visible Content strips `<oai-mem-citation>` tags, while this field keeps
	// parsed provenance data for clients that want to render it.
	MemoryCitation *MemoryCitation
}

type ToolCall struct {
	CallID    string
	Name      string
	Arguments json.RawMessage
}

type ToolResult struct {
	CallID string
	Name   string
	Output string
	// Parts carries structured model-visible content such as image data. Codex
	// represents view_image output as content items rather than plain text; Dexco
	// keeps that extension point alongside Output so existing text tools remain
	// source-compatible while multimodal tools can adopt Codex behavior.
	Parts   []ContentPart
	Success bool
	// PlanUpdate carries the structured client event produced by the
	// `update_plan` tool. Codex keeps this as a separate PlanUpdate event while
	// still returning a model-visible tool output of "Plan updated"; Dexco
	// attaches the metadata to the tool result so the runner can preserve that
	// same event/output split without special-casing dispatch.
	PlanUpdate *PlanUpdate
}

type ToolLifecyclePhase string

const (
	ToolLifecycleStart  ToolLifecyclePhase = "start"
	ToolLifecycleFinish ToolLifecyclePhase = "finish"
)

type ToolLifecycleOutcome string

const (
	ToolLifecycleOutcomeCompleted ToolLifecycleOutcome = "completed"
	ToolLifecycleOutcomeFailed    ToolLifecycleOutcome = "failed"
)

// ToolLifecycleEvent is Dexco's library-level adaptation of Codex's
// ToolLifecycleContributor callbacks. Codex records both the start of a tool
// dispatch and a finish outcome that distinguishes "the handler returned a
// model-visible unsuccessful result" from "the handler itself failed"; Dexco
// exposes the same distinction without importing Codex's extension registry.
type ToolLifecycleEvent struct {
	Phase           ToolLifecyclePhase
	Call            ToolCall
	Outcome         ToolLifecycleOutcome
	Success         bool
	HandlerExecuted bool
	Result          *ToolResult
}

type ContentPartKind string

const (
	ContentPartText      ContentPartKind = "text"
	ContentPartImage     ContentPartKind = "image"
	ContentPartEncrypted ContentPartKind = "encrypted_content"
)

type ContentPart struct {
	Kind ContentPartKind
	Text string
	// ImageURL is the canonical model-visible image payload. Codex Rust sends
	// tool-output images as Responses `input_image` content items whose
	// `image_url` is a data URL. Dexco keeps the same normalized shape here so
	// provider adapters can forward rich tool output without re-encoding it.
	ImageURL string
	// ImageData and MIMEType are accepted as adapter-facing inputs for MCP-style
	// image content (`data` + `mimeType`). NormalizeToolResultParts folds them
	// into ImageURL and clears the raw fields, matching Codex's
	// convert_mcp_content_to_items behavior.
	ImageData string
	MIMEType  string
	// Detail mirrors Codex's image detail metadata. For tool results the default
	// is `high`, and `original` is preserved for callers that need lossless image
	// inspection.
	Detail string
	Path   string
	// EncryptedContent mirrors Codex's `encrypted_content` output item. It is
	// intentionally ignored by ToolResultPartsText because it is opaque to
	// human-readable legacy string surfaces.
	EncryptedContent string
}

const (
	defaultToolResultImageDetail = "high"
	defaultToolResultImageMIME   = "application/octet-stream"
)

const RemoteImageURLToolResultError = "remote image URLs are not supported; use an inline data URL instead"

// NormalizeToolResultParts canonicalizes rich tool-output content using the
// same rules Codex Rust applies before sending FunctionCallOutputContentItem
// arrays back to the model:
//   - text parts are preserved as-is
//   - image data without a `data:` prefix is converted to a data URL
//   - missing or unsupported image detail defaults to `high`
//   - encrypted content remains structured and opaque
//
// Keeping this logic in the model package gives Dexco embedders one stable
// adoption point when Codex changes its rich tool-output semantics.
func NormalizeToolResultParts(parts []ContentPart) []ContentPart {
	if len(parts) == 0 {
		return nil
	}

	normalized := make([]ContentPart, 0, len(parts))
	for _, part := range parts {
		if part.Kind == ContentPartImage {
			if part.ImageURL == "" && part.ImageData != "" {
				part.ImageURL = part.ImageData
			}
			if part.ImageURL != "" && !strings.HasPrefix(part.ImageURL, "data:") {
				mimeType := strings.TrimSpace(part.MIMEType)
				if mimeType == "" {
					mimeType = defaultToolResultImageMIME
				}
				part.ImageURL = "data:" + mimeType + ";base64," + part.ImageURL
			}
			part.ImageData = ""
			part.MIMEType = ""

			switch strings.ToLower(strings.TrimSpace(part.Detail)) {
			case "auto":
				part.Detail = "auto"
			case "low":
				part.Detail = "low"
			case "high":
				part.Detail = "high"
			case "original":
				part.Detail = "original"
			default:
				part.Detail = defaultToolResultImageDetail
			}
		}
		normalized = append(normalized, part)
	}
	return normalized
}

// ToolResultPartsText is Dexco's equivalent of Codex Rust's
// function_call_output_content_items_to_text helper. It provides a lossy
// text-only fallback for logs, telemetry, and legacy library callers while the
// structured Parts slice remains the authoritative model-visible payload.
func ToolResultPartsText(parts []ContentPart) string {
	textSegments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Kind == ContentPartText && strings.TrimSpace(part.Text) != "" {
			textSegments = append(textSegments, part.Text)
		}
	}
	return strings.Join(textSegments, "\n")
}

// ToolResultFromContentParts builds a ToolResult from Codex-style structured
// output content. Output is populated with the same lossy text fallback Codex
// uses for human-readable surfaces, while Parts keeps text, image, and encrypted
// content intact for model adapters.
func ToolResultFromContentParts(callID string, name string, parts []ContentPart, success bool) ToolResult {
	if containsRemoteImageURL(parts) {
		return remoteImageURLToolResult(callID, name)
	}
	normalized := NormalizeToolResultParts(parts)
	return ToolResult{
		CallID:  callID,
		Name:    name,
		Output:  ToolResultPartsText(normalized),
		Parts:   normalized,
		Success: success,
	}
}

// ToolResultWithoutRemoteImageURLs returns a model-visible error result if rich
// tool output contains a remote image URL. Codex app-server accepts inline data
// URLs from dynamic tools, but rejects `http:`/`https:` image outputs before
// they reach the model; otherwise the model could fetch data outside the
// tool-call contract. Dexco keeps this as an adapter-facing helper because
// remote image URLs are still valid user input images, while tool-result images
// should be inline artifacts.
func ToolResultWithoutRemoteImageURLs(result ToolResult) ToolResult {
	if !containsRemoteImageURL(result.Parts) {
		return result
	}
	return remoteImageURLToolResult(result.CallID, result.Name)
}

func remoteImageURLToolResult(callID string, name string) ToolResult {
	return ToolResult{
		CallID: callID,
		Name:   name,
		Output: RemoteImageURLToolResultError,
		Parts: []ContentPart{{
			Kind: ContentPartText,
			Text: RemoteImageURLToolResultError,
		}},
		Success: false,
	}
}

func containsRemoteImageURL(parts []ContentPart) bool {
	for _, part := range parts {
		if part.Kind == ContentPartImage && isRemoteImageURL(part.ImageURL) {
			return true
		}
	}
	return false
}

func isRemoteImageURL(imageURL string) bool {
	scheme, _, ok := strings.Cut(imageURL, ":")
	return ok && (strings.EqualFold(scheme, "http") || strings.EqualFold(scheme, "https"))
}

const imageInputUnsupportedPlaceholder = "<image content omitted because you do not support image input>"

// ToolResultWithoutImageInput returns a model-send copy for adapters targeting
// models that cannot accept image input. Codex performs the same sanitation for
// MCP tool results when the selected model lacks image-input support: each
// image content item is replaced in-place with a text placeholder so the model
// can still reason about the omitted artifact without receiving unsupported
// bytes. Dexco keeps the durable ToolResult unchanged and lets provider
// adapters call this helper only for the request copy that needs sanitation.
func ToolResultWithoutImageInput(result ToolResult) ToolResult {
	if len(result.Parts) == 0 {
		return result
	}
	result.Parts = contentPartsWithoutImageInput(result.Parts)
	result.Output = ToolResultPartsText(result.Parts)
	if result.PlanUpdate != nil {
		planUpdate := *result.PlanUpdate
		planUpdate.Plan = append([]PlanStep(nil), planUpdate.Plan...)
		result.PlanUpdate = &planUpdate
	}
	return result
}

func contentPartsWithoutImageInput(parts []ContentPart) []ContentPart {
	if len(parts) == 0 {
		return nil
	}
	sanitized := make([]ContentPart, 0, len(parts))
	for _, part := range NormalizeToolResultParts(parts) {
		if part.Kind == ContentPartImage {
			sanitized = append(sanitized, ContentPart{
				Kind: ContentPartText,
				Text: imageInputUnsupportedPlaceholder,
			})
			continue
		}
		sanitized = append(sanitized, part)
	}
	return sanitized
}

// TruncateToolResultOutput applies Codex's model-visible history guardrail for
// arbitrary tool outputs. Rust Codex truncates FunctionCallOutput and
// CustomToolCallOutput when recording history so a single tool cannot consume an
// unbounded future prompt. Dexco mirrors that at the provider-neutral
// ToolResult boundary: metadata, image parts, encrypted parts, and success state
// are preserved, while text-bearing fields receive explicit truncation markers.
func TruncateToolResultOutput(result ToolResult, maxChars int) ToolResult {
	if maxChars < 0 {
		return result
	}
	result.Output = truncateToolResultText(result.Output, maxChars)
	if len(result.Parts) == 0 {
		return result
	}
	result.Parts = truncateToolResultParts(result.Parts, maxChars)
	return result
}

func truncateToolResultParts(parts []ContentPart, maxChars int) []ContentPart {
	truncated := make([]ContentPart, 0, len(parts))
	remaining := maxChars
	omittedTextParts := 0
	for _, part := range parts {
		if part.Kind != ContentPartText {
			truncated = append(truncated, part)
			continue
		}
		if remaining <= 0 {
			omittedTextParts++
			continue
		}
		textRunes := []rune(part.Text)
		if len(textRunes) <= remaining {
			truncated = append(truncated, part)
			remaining -= len(textRunes)
			continue
		}
		part.Text = truncateToolResultText(part.Text, remaining)
		if part.Text == "" {
			omittedTextParts++
		} else {
			truncated = append(truncated, part)
		}
		remaining = 0
	}
	if omittedTextParts > 0 {
		truncated = append(truncated, ContentPart{
			Kind: ContentPartText,
			Text: fmt.Sprintf("[omitted %d text tool-result parts ...]", omittedTextParts),
		})
	}
	return truncated
}

func truncateToolResultText(text string, maxChars int) string {
	runes := []rune(text)
	if maxChars < 0 || len(runes) <= maxChars || alreadyTruncatedToolResultText(text) {
		return text
	}
	if maxChars == 0 {
		return fmt.Sprintf(
			"Warning: truncated tool output (original character count: %d)\n\n... %d characters truncated ...",
			len(runes),
			len(runes),
		)
	}
	if maxChars < 2 {
		return fmt.Sprintf(
			"Warning: truncated tool output (original character count: %d)\n\n%s\n... %d characters truncated ...",
			len(runes),
			string(runes[:maxChars]),
			len(runes)-maxChars,
		)
	}
	headChars := maxChars / 2
	tailChars := maxChars - headChars
	omitted := len(runes) - headChars - tailChars
	return fmt.Sprintf(
		"Warning: truncated tool output (original character count: %d)\n\n%s\n... %d characters truncated ...\n%s",
		len(runes),
		string(runes[:headChars]),
		omitted,
		string(runes[len(runes)-tailChars:]),
	)
}

func alreadyTruncatedToolResultText(text string) bool {
	if !strings.Contains(text, "Warning: truncated tool output") &&
		!strings.Contains(text, "Warning: truncated output") {
		return false
	}
	return strings.Contains(text, "characters truncated") ||
		strings.Contains(text, "chars truncated") ||
		strings.Contains(text, "tokens truncated") ||
		strings.Contains(text, "bytes truncated")
}

type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any
}

type PlanStepStatus string

const (
	PlanStepPending    PlanStepStatus = "pending"
	PlanStepInProgress PlanStepStatus = "in_progress"
	PlanStepCompleted  PlanStepStatus = "completed"
)

type PlanStep struct {
	Step   string         `json:"step"`
	Status PlanStepStatus `json:"status"`
}

// PlanUpdate is Dexco's provider-neutral form of Codex's PlanUpdate event.
// Codex validates the update_plan payload, emits this event for clients, then
// sends "Plan updated" back to the model as the tool result. Keeping the same
// structured payload here lets library callers render task checklists while the
// transcript stays compatible with Codex's loop semantics.
type PlanUpdate struct {
	Explanation string     `json:"explanation,omitempty"`
	Plan        []PlanStep `json:"plan"`
}

type WebSearchActionKind string

const (
	WebSearchActionOther      WebSearchActionKind = "other"
	WebSearchActionSearch     WebSearchActionKind = "search"
	WebSearchActionOpenPage   WebSearchActionKind = "open_page"
	WebSearchActionFindInPage WebSearchActionKind = "find_in_page"
)

// WebSearchAction is Dexco's provider-neutral form of Codex web-search turn
// actions. Codex parses these from Responses API web_search_call items; Dexco
// keeps the normalized action in history so library clients can render or replay
// it without depending on a specific provider payload.
type WebSearchAction struct {
	Kind    WebSearchActionKind
	Query   string
	Queries []string
	URL     string
	Pattern string
}

func (a WebSearchAction) DisplayQuery() string {
	switch a.Kind {
	case WebSearchActionSearch:
		if a.Query != "" {
			return a.Query
		}
		return strings.Join(a.Queries, "\n")
	case WebSearchActionOpenPage:
		return a.URL
	case WebSearchActionFindInPage:
		if a.Pattern == "" && a.URL == "" {
			return ""
		}
		return "'" + a.Pattern + "' in " + a.URL
	case WebSearchActionOther:
		return ""
	default:
		return ""
	}
}

type WebSearch struct {
	ID     string
	Status string
	Query  string
	Action WebSearchAction
}

type WebSearchMode string

const (
	WebSearchModeDisabled WebSearchMode = "disabled"
	WebSearchModeCached   WebSearchMode = "cached"
	WebSearchModeLive     WebSearchMode = "live"
	WebSearchModeIndexed  WebSearchMode = "indexed"
)

type WebSearchUserLocation struct {
	Country  string
	Region   string
	City     string
	Timezone string
}

// WebSearchRequest is Dexco's provider-neutral request-side equivalent of
// Codex's hosted `web_search` tool configuration. Codex serializes this into
// Responses API fields such as external_web_access, index_gated_web_access,
// filters.allowed_domains, search_context_size, and user_location. Dexco keeps
// that resolved metadata on Prompt so model adapters can encode it for their
// provider without coupling the loop to OpenAI's wire schema.
type WebSearchRequest struct {
	Mode                WebSearchMode
	ExternalWebAccess   bool
	IndexGatedWebAccess bool
	SearchContextSize   string
	AllowedDomains      []string
	UserLocation        *WebSearchUserLocation
}

func NormalizeWebSearchRequest(request *WebSearchRequest) *WebSearchRequest {
	if request == nil {
		return nil
	}
	normalized := CloneWebSearchRequest(request)
	if normalized.Mode == "" {
		normalized.Mode = WebSearchModeCached
	}
	switch normalized.Mode {
	case WebSearchModeDisabled:
		return nil
	case WebSearchModeCached:
		normalized.ExternalWebAccess = false
		normalized.IndexGatedWebAccess = false
	case WebSearchModeLive:
		normalized.ExternalWebAccess = true
		normalized.IndexGatedWebAccess = false
	case WebSearchModeIndexed:
		normalized.ExternalWebAccess = true
		normalized.IndexGatedWebAccess = true
	default:
		normalized.Mode = WebSearchModeCached
		normalized.ExternalWebAccess = false
		normalized.IndexGatedWebAccess = false
	}
	return normalized
}

func CloneWebSearchRequest(request *WebSearchRequest) *WebSearchRequest {
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

// ImageGeneration is Dexco's provider-neutral form of Codex's hosted
// image_generation_call item. Codex may also save the base64 result to an
// artifact path; Dexco preserves the optional path if a model adapter supplies
// one but intentionally leaves file persistence to the embedding application.
type ImageGeneration struct {
	ID            string
	Status        string
	RevisedPrompt string
	Result        string
	SavedPath     string
}

type ModelErrorKind string

const (
	ModelErrorUnknown     ModelErrorKind = "unknown"
	ModelErrorCyberPolicy ModelErrorKind = "cyber_policy"
	ModelErrorQuota       ModelErrorKind = "quota_exceeded"
)

// ModelError is the library-facing equivalent of Codex's normalized provider
// errors. Transport adapters can wrap provider-specific failures in this type so
// the loop can preserve Codex retry semantics without depending on HTTP status
// codes or OpenAI-specific response envelopes.
type ModelError struct {
	Kind      ModelErrorKind
	Message   string
	Retryable bool
}

func NewModelError(kind ModelErrorKind, message string, retryable bool) *ModelError {
	return &ModelError{
		Kind:      kind,
		Message:   message,
		Retryable: retryable,
	}
}

func (e *ModelError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Kind != "" {
		return string(e.Kind)
	}
	return string(ModelErrorUnknown)
}

// ToolRisk is Dexco's compact equivalent of Codex's richer execution policy
// classification. Codex combines approval policy, sandbox policy, exec policy
// rules, network policy, and tool-specific metadata; Dexco keeps the library
// surface smaller by letting each handler classify the risk it knows about.
type ToolRisk string

const (
	ToolRiskUnknown          ToolRisk = "unknown"
	ToolRiskReadOnly         ToolRisk = "read_only"
	ToolRiskUserInteraction  ToolRisk = "user_interaction"
	ToolRiskCommandExecution ToolRisk = "command_execution"
	ToolRiskWorkspaceWrite   ToolRisk = "workspace_write"
	ToolRiskNetwork          ToolRisk = "network"
	ToolRiskDestructive      ToolRisk = "destructive"
)

// ApprovalRequirement is the handler-side answer to "does this specific call
// need a gate?" Codex computes this from tool runtime policy and sandbox
// permissions; Dexco exposes it directly so library users can plug in their own
// policy engine without importing Codex internals.
type ApprovalRequirement string

const (
	ApprovalRequirementNone     ApprovalRequirement = "none"
	ApprovalRequirementRequired ApprovalRequirement = "required"
	ApprovalRequirementDenied   ApprovalRequirement = "denied"
)

// ApprovalPolicy is the runner-side policy that decides how broadly to enforce
// per-call requirements. The default remains AllowAll so Dexco stays backwards
// compatible as a library; callers opt into Codex-style gates explicitly.
type ApprovalPolicy string

const (
	ApprovalPolicyAllowAll            ApprovalPolicy = "allow_all"
	ApprovalPolicyRequireForSensitive ApprovalPolicy = "require_for_sensitive"
	ApprovalPolicyRequireForAll       ApprovalPolicy = "require_for_all"
	ApprovalPolicyDenyAll             ApprovalPolicy = "deny_all"
)

// ApprovalDecision mirrors Codex's approval outcomes at the point Dexco needs
// them. "NoDecision" is important: it preserves Codex's precedence order where
// permission hooks can decline to answer and let the user/guardian reviewer run.
type ApprovalDecision string

const (
	ApprovalDecisionNoDecision ApprovalDecision = "no_decision"
	ApprovalDecisionApproved   ApprovalDecision = "approved"
	ApprovalDecisionDenied     ApprovalDecision = "denied"
)

// PermissionGrantScope mirrors Codex's request_permissions response scope, but
// Dexco keeps grants provider-neutral. A turn grant is cleared when the current
// runner turn exits; a session grant can be reused by later turns that share the
// same PermissionGrantStore.
type PermissionGrantScope string

const (
	PermissionGrantScopeTurn    PermissionGrantScope = "turn"
	PermissionGrantScopeSession PermissionGrantScope = "session"
)

// PermissionGrant is Dexco's compact analogue of Codex's additional permission
// profile. Codex stores structured filesystem/network overlays; Dexco is a
// library and does not own a sandbox, so tools expose stable grant keys for the
// side effects they know how to classify.
type PermissionGrant struct {
	Key         string `json:"key"`
	Description string `json:"description,omitempty"`
}

// ToolGuardrail is the portable guardrail payload passed from a tool handler to
// the runner. Codex has more sources of truth, but the stable invariant is the
// same: classify the pending action before dispatch, then decide whether a gate
// is required before any side effect runs.
type ToolGuardrail struct {
	Risk                ToolRisk
	ApprovalRequirement ApprovalRequirement
	Reason              string
	// PermissionGrantKey connects guardrails with request_permissions grants.
	// If a prior turn/session grant with this key exists, Dexco skips the normal
	// approval callback for this call. This adapts Codex's "additional
	// permissions preapproved" flow without baking filesystem or network policy
	// into the library core.
	PermissionGrantKey string
	Metadata           map[string]any
}

// ToolApprovalRequest is emitted to hooks/reviewers and to client event sinks.
// It intentionally includes the normalized call and computed reason so future
// Codex approval improvements can be adopted by enriching this struct rather
// than changing the tool dispatch contract.
type ToolApprovalRequest struct {
	TurnID    string
	Call      ToolCall
	Guardrail ToolGuardrail
	Policy    ApprovalPolicy
	Reason    string
}

type Prompt struct {
	History           []Item
	Instructions      string
	DeveloperMessages []string
	Tools             []ToolSpec
	// TurnState is Dexco's provider-neutral adaptation of Codex's
	// `x-codex-turn-state` sticky-routing token. Provider adapters may encode
	// this as an HTTP header, WebSocket client metadata, or another transport
	// field, but it is not conversation history. The runner scopes it to one
	// logical turn, replays the first provider-supplied value on same-turn
	// follow-up requests, and clears it for the next user turn.
	TurnState string
	// WebSearch carries the resolved hosted web-search request metadata for this
	// sampling request. It is prompt metadata, not durable history.
	WebSearch *WebSearchRequest
	// OutputSchema mirrors Codex's final_output_json_schema request field at the
	// library boundary. Dexco does not prescribe an HTTP provider encoding, but
	// model clients receive this schema on every sampling request for the turn so
	// they can request structured JSON output the same way Codex does.
	OutputSchema json.RawMessage
}

type UserInput struct {
	Content string
	// Parts mirrors Codex's multimodal user-input content. Text-only callers can
	// keep using Content, while image-aware callers can attach data URLs or local
	// paths without depending on a specific Responses API wire format.
	Parts []ContentPart
}

type AdditionalContextKind string

const (
	AdditionalContextUntrusted   AdditionalContextKind = "untrusted"
	AdditionalContextApplication AdditionalContextKind = "application"
)

// AdditionalContextEntry is per-turn model context supplied by the embedding
// application. Codex uses this for browser/app/automation state: it is visible
// to the model, but it is not treated as the user's chat message.
type AdditionalContextEntry struct {
	Value string
	Kind  AdditionalContextKind
}

type OpUserInput struct {
	Input UserInput
	// WebSearch optionally overrides the session web-search request metadata for
	// this turn. Nil uses Config.WebSearch; Mode disabled omits web-search
	// metadata from Prompt.
	WebSearch *WebSearchRequest
	// AdditionalContext mirrors Codex's per-turn additional_context map. Dexco
	// renders new or changed entries as context items before the user input,
	// retains prior context in model history, and avoids duplicating entries that
	// are unchanged from the previous successful turn.
	AdditionalContext map[string]AdditionalContextEntry
	// OutputSchema is optional per-turn structured-output guidance. It is prompt
	// metadata, not conversation history; failed turns and later turns should not
	// accidentally persist it unless the caller supplies it again.
	OutputSchema json.RawMessage
}

type Turn struct {
	ID                string
	History           []Item
	Instructions      string
	DeveloperMessages []string
	WebSearch         *WebSearchRequest
	OutputSchema      json.RawMessage
	Status            TurnStatus
}

// TurnMetrics is Dexco's provider-neutral adaptation of Codex turn timing
// telemetry. It intentionally reports loop-level milestones rather than
// provider wire details: when the turn started, whether the model produced
// observable output/message content, and how time was split across sampling,
// retries, tool blocking, and local overhead.
type TurnMetrics struct {
	StartedAt          time.Time
	HasFirstOutput     bool
	TimeToFirstOutput  time.Duration
	HasFirstMessage    bool
	TimeToFirstMessage time.Duration
	Profile            TurnProfile
}

type TurnProfile struct {
	BeforeFirstSampling     time.Duration
	Sampling                time.Duration
	BetweenSamplingOverhead time.Duration
	ToolBlocking            time.Duration
	AfterLastSampling       time.Duration
	SamplingRequestCount    int
	SamplingRetryCount      int
}

type TurnStatus string

const (
	TurnStatusRunning   TurnStatus = "running"
	TurnStatusCompleted TurnStatus = "completed"
	TurnStatusFailed    TurnStatus = "failed"
)

type ResponseEventType string

const (
	EventCreated                   ResponseEventType = "created"
	EventOutputItemAdded           ResponseEventType = "output_item_added"
	EventOutputTextDelta           ResponseEventType = "output_text_delta"
	EventReasoningDelta            ResponseEventType = "reasoning_delta"
	EventReasoningSummaryDelta     ResponseEventType = "reasoning_summary_delta"
	EventReasoningSummaryPartAdded ResponseEventType = "reasoning_summary_part_added"
	EventReasoningContentDelta     ResponseEventType = "reasoning_content_delta"
	EventToolCallInputDelta        ResponseEventType = "tool_call_input_delta"
	EventOutputItemDone            ResponseEventType = "output_item_done"
	EventServerModel               ResponseEventType = "server_model"
	EventModelVerifications        ResponseEventType = "model_verifications"
	EventTurnModerationMetadata    ResponseEventType = "turn_moderation_metadata"
	EventSafetyBuffering           ResponseEventType = "safety_buffering"
	EventServerReasoningIncluded   ResponseEventType = "server_reasoning_included"
	EventRateLimits                ResponseEventType = "rate_limits"
	EventModelsEtag                ResponseEventType = "models_etag"
	EventCompleted                 ResponseEventType = "completed"
)

type ResponseEvent struct {
	Type         ResponseEventType
	Delta        string
	Item         *Item
	ItemID       string
	CallID       string
	SummaryIndex *int
	ContentIndex *int
	EndTurn      *bool
	// TurnState carries the provider-supplied state token that should be replayed
	// on later sampling requests in the same turn. Codex stores the transport
	// value in an OnceLock so later response metadata cannot overwrite it; Dexco
	// mirrors that first-value-wins behavior in the runner.
	TurnState  string
	TokenUsage *TokenUsage
	Metadata   map[string]any
}

type TokenUsage struct {
	InputTokens              int64
	CachedInputTokens        int64
	OutputTokens             int64
	ReasoningOutputTokens    int64
	TotalTokens              int64
	EstimatedContextTokens   int64
	EstimatedRemainingTokens int64
}

type ClientEventType string

const (
	ClientEventTurnStarted          ClientEventType = "turn_started"
	ClientEventTextDelta            ClientEventType = "text_delta"
	ClientEventReasoning            ClientEventType = "reasoning_delta"
	ClientEventToolCall             ClientEventType = "tool_call"
	ClientEventToolResult           ClientEventType = "tool_result"
	ClientEventWebSearch            ClientEventType = "web_search"
	ClientEventHookPrompt           ClientEventType = "hook_prompt"
	ClientEventImageGeneration      ClientEventType = "image_generation"
	ClientEventPlanUpdate           ClientEventType = "plan_update"
	ClientEventToolApprovalRequest  ClientEventType = "tool_approval_request"
	ClientEventToolApprovalDecision ClientEventType = "tool_approval_decision"
	ClientEventTurnCompleted        ClientEventType = "turn_completed"
	ClientEventResponseEvent        ClientEventType = "response_event"
	ClientEventModelRetry           ClientEventType = "model_retry"
)

type ClientEvent struct {
	Type   ClientEventType
	TurnID string
	// ItemID preserves provider item metadata for streaming events that need to
	// be reconciled with completed history items. Codex includes this on
	// assistant text and reasoning-content deltas; Dexco keeps it optional so
	// older/simple clients can ignore it.
	ItemID              string
	Turn                *Turn
	Delta               string
	ToolCall            *ToolCall
	ToolResult          *ToolResult
	WebSearch           *WebSearch
	HookPrompt          *HookPrompt
	ImageGeneration     *ImageGeneration
	PlanUpdate          *PlanUpdate
	ToolApprovalRequest *ToolApprovalRequest
	ApprovalDecision    ApprovalDecision
	ResponseEvent       *ResponseEvent
	RetryAttempt        int
	RetryError          string
}

func UserInputItem(content string) Item {
	return UserInputItemWithParts(content, nil)
}

func UserInputItemWithParts(content string, parts []ContentPart) Item {
	return Item{
		Kind:    ItemUserInput,
		Role:    "user",
		Content: content,
		Parts:   append([]ContentPart(nil), parts...),
	}
}

func ContextItem(role string, key string, content string) Item {
	return Item{
		Kind:       ItemContext,
		Role:       role,
		ContextKey: key,
		Content:    content,
	}
}

func TurnAbortedItem() Item {
	return ContextItem("user", "turn_aborted", "<turn_aborted>\n"+TurnAbortedGuidance+"\n</turn_aborted>")
}

func AssistantMessageItem(content string) Item {
	visible, citation := StripMemoryCitations(content)
	return Item{
		Kind:           ItemAssistantMessage,
		Role:           "assistant",
		Content:        visible,
		MemoryCitation: citation,
	}
}

// AssistantMessageItemFromParts mirrors Codex's agent-message parsing nuance:
// older rollouts can contain assistant messages encoded as input_text rather
// than output_text. Dexco's ContentPart is already provider-neutral, so any text
// part is accepted and flattened for the legacy Item.Content view while callers
// that need exact multimodal structure should use Parts on user/tool items.
func AssistantMessageItemFromParts(parts []ContentPart) Item {
	return AssistantMessageItem(flattenTextParts(parts))
}

func ToolCallItem(call ToolCall) Item {
	return Item{
		Kind:     ItemToolCall,
		ToolCall: &call,
	}
}

func ToolResultItem(result ToolResult) Item {
	return Item{
		Kind:       ItemToolResult,
		ToolResult: &result,
	}
}

func WebSearchItem(id string, status string, action WebSearchAction) Item {
	search := WebSearch{
		ID:     id,
		Status: status,
		Query:  action.DisplayQuery(),
		Action: action,
	}
	return Item{
		Kind:      ItemWebSearch,
		WebSearch: &search,
	}
}

func ImageGenerationItem(id string, status string, revisedPrompt string, result string, savedPath string) Item {
	image := ImageGeneration{
		ID:            id,
		Status:        status,
		RevisedPrompt: revisedPrompt,
		Result:        result,
		SavedPath:     savedPath,
	}
	return Item{
		Kind:            ItemImageGeneration,
		ImageGeneration: &image,
	}
}

func ReasoningItem(content string) Item {
	return Item{
		Kind:    ItemReasoning,
		Role:    "assistant",
		Content: content,
	}
}
