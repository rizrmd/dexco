# CapabilityProfile design (WIP)

This document defines Dexco's `CapabilityProfile` layer. The goal is to make
Dexco usable for multi-user chatbot/product agents without confusing that use
case with Codex-style local coding automation.

## Status

This is the design contract for Dexco's first `CapabilityProfile`
implementation. The root profile construction API, profile-aware handlers,
explicit chat/coding workflow helpers, capability checks, policy decisions, and
progress narration are implemented; future changes should update this document
before changing code.

Implementation should follow this document unless the design is updated first.
If future Codex improvements change how profiles, permissions, tools, or
guardrails should work, update this document before changing code.

## Implementation decisions

These decisions are fixed for the first implementation:

- `Principal` and `CapabilityProfile` are exported Dexco root-package types.
- `CapabilityProfile` stores `Principal`. `context.Context` is only used to
  deliver profile/principal data to handlers, hooks, and reviewers during a
  call.
- Capability matching is exact-match only. Wildcards are not supported in
  Dexco v1; callers must expand wildcards before constructing the profile.
- Capability bundles are optional host-application authoring helpers. Dexco
  receives only the expanded exact capabilities and does not evaluate bundle
  membership at runtime.
- Profile changes are not allowed mid-session. If effective identity,
  capabilities, persona, or visible tools change, the host creates a new
  session.
- `CapabilityProfile` is the single construction recipe for profile sessions.
  Callers that need lower-level control should keep using `NewRouter`,
  `NewRunnerWithOptions`, and `NewSession` directly.
- Progress narration is a client-event feature, not a handler-side emission
  feature. Handlers provide safe progress hints; Dexco controls timing.
- Denied tool calls produce both a safe model-visible tool result and a
  structured client event.
- Profile policy always evaluates the final tool call after any allowed hook
  mutation. Hooks and reviewers can be stricter than profile policy, but they
  cannot bypass required capabilities.

## Problem

Dexco currently exposes a Codex-like LLM loop with routers, handlers, hooks,
tool execution, and guardrails. That is useful for building agent runtimes, but
it does not directly answer product questions such as:

- Who is the authenticated user?
- Which roles does that user have?
- Which actions are allowed for this user in this tenant?
- Which tools should be visible to the model?
- Which persona and tone should the assistant use?
- Which actions need automatic policy review before execution?

Those concerns are different from a local coding agent. A chatbot embedded in a
business product needs explicit identity, capabilities, persona, tool exposure,
and policy enforcement.

## Design goals

- Keep authentication and RBAC resolution in the host application.
- Give Dexco a clear profile object that describes behavior for one session.
- Make tool exposure explicit and auditable.
- Enforce permissions before tools run.
- Treat prompt/persona as guidance, not security.
- Support users with multiple roles.
- Track current active work so long-running operations can produce accurate,
  optional progress narration.
- Keep router semantics simple: a router remains only a tool registry and
  dispatcher.
- Preserve the Codex adaptation seam so future Codex improvements can be
  adopted without rewriting product-policy code.

## Non-goals

- Dexco should not authenticate users.
- Dexco should not fetch roles from a database.
- Dexco should not decide business permissions from raw role names.
- Dexco should not infer a user's role from conversation text.
- Dexco should not make prompt instructions the source of authorization.
- Dexco should not infer hidden model reasoning from silence. It can only
  report observable runtime state.
- Dexco should not emit progress narration for every state change. Narration is
  delayed and optional.
- Dexco should not turn builtin tool groups into a separate top-level concept
  unless they carry policy beyond handler composition.

## Concept model

```text
authenticated request
        |
        v
host application resolves identity and expands roles/bundles
        |
        v
Principal{user, tenant, roles, capabilities}
        |
        v
ProfileResolver selects CapabilityProfile
        |
        v
Dexco session
        |
        +--> prompt persona/instructions
        +--> router with visible tools
        +--> guardrail reviewer
        +--> domain tool handlers
        +--> optional progress narration
```

## Principal

`Principal` is the host-resolved identity that Dexco receives. It should be
created from trusted application authentication and authorization state.

Proposed shape:

```go
type Principal struct {
	UserID               string
	TenantID             string
	Roles                []string
	Capabilities         []string
	ActiveRole           string
	CapabilitySetVersion string
	Metadata             map[string]string
}
```

`Roles` are descriptive inputs such as `management`, `sales`, or `customer`.
`Capabilities` are normalized authorization outputs such as
`reports.sensitive.read`. Dexco policy should rely on capabilities, not raw
role names.

`ActiveRole` is optional. It lets a multi-role user intentionally operate in a
narrower context, such as a management user acting as sales for a specific
conversation.

`CapabilitySetVersion` is a host-provided audit value. It should identify the
authorization snapshot used to create the profile, such as a policy version,
role assignment version, or timestamped RBAC evaluation ID.

`Metadata` must not contain secrets. Treat it as operational metadata for
logging, routing, or audit correlation.

Dexco should expose helpers so handlers, hooks, and reviewers can read the
session identity from call contexts:

```go
func PrincipalFromContext(ctx context.Context) (Principal, bool)
func CapabilityProfileFromContext(ctx context.Context) (CapabilityProfile, bool)
```

These helpers are not the source of truth. The profile remains the source of
truth; context is only the delivery path during a call. Values returned from
context are read-only snapshots. Handlers, hooks, and reviewers must not mutate
them or assume mutation changes the active session.

## Capability keys

Capability keys use dot-separated parts:

```text
<domain>.<action>
<domain>.<resource>.<action>
```

Examples:

```text
sales.create
sales.order.cancel
sales.discount.request
sales.discount.approve
reports.sensitive.read
reports.executive.read
customers.profile.read
customers.profile.update
handoff.admin.create
```

The meaning of `reports.sensitive.read` is:

```text
domain   = reports
resource = sensitive
action   = read
```

This means the principal is allowed to read sensitive reports.

Use a resource segment when `domain.action` is too broad. A resource should
exist when it needs separate granting, denial, audit, or review.

Good resource candidates:

- Different roles can access different subsets.
- The data has different sensitivity levels.
- The operation has different business risk.
- Audit logs need a meaningful object category.
- Policy is expected to evolve independently.

Avoid over-modeling resources that do not change policy. For example,
`sales.create` is preferable to `sales.button.click`.

## Capability bundles

Capability bundles are optional host-application helpers for reducing
permission-management overhead. They are not required by Dexco and are not a
Dexco runtime authorization primitive.

Use bundles when multiple roles need the same group of exact capabilities:

```yaml
bundles:
  reports_reader:
    - reports.public.read
    - reports.executive.read

  sales_operator:
    - sales.create
    - sales.leads.read
    - customers.profile.read

roles:
  management:
    bundles:
      - reports_reader
      - sales_operator
    capabilities:
      - discounts.approve
```

The host expands bundles before constructing `Principal`:

```go
Principal{
	Roles: []string{"management"},
	Capabilities: []string{
		"reports.public.read",
		"reports.executive.read",
		"sales.create",
		"sales.leads.read",
		"customers.profile.read",
		"discounts.approve",
	},
	CapabilitySetVersion: "rbac-2026-07-01.3",
}
```

Capabilities that do not come from a bundle are still valid. They are direct
grants. Direct grants are useful for role-specific exceptions, temporary
elevations, feature-flagged permissions, tenant-specific policies, and
migration periods.

Runtime rule:

```text
role templates, bundles, wildcards, and inheritance:
  resolved by the host application

Principal.Capabilities:
  exact expanded capability strings only

Dexco authorization:
  exact capability matching only
```

For audit, the host may log or store where each exact capability came from,
such as `role`, `bundle`, `direct`, or `temporary_grant`. Dexco does not need
that provenance to authorize a tool call; it only needs the final exact
capability set and `CapabilitySetVersion`.

## Capability matching

Dexco v1 should use exact capability matching only.

```text
principal has reports.sensitive.read
required capability reports.sensitive.read -> allowed
required capability reports.*.read        -> invalid, not matched
required capability reports.read          -> denied
```

Wildcard semantics belong in the host authorization system. If the application
stores wildcard grants such as `reports.*.read`, it must expand them into exact
capability strings before constructing `Principal.Capabilities`.

Proposed requirement shape:

```go
type CapabilityRequirement struct {
	All []string
	Any []string
}
```

Evaluation rules:

```text
All:
  every listed capability must be present

Any:
  if non-empty, at least one listed capability must be present

All + Any:
  all All capabilities must be present, and at least one Any capability must be
  present

empty All and empty Any:
  no capability required
```

Examples:

```go
CapabilityRequirement{
	All: []string{"discounts.approve", "customers.profile.read"},
}

CapabilityRequirement{
	Any: []string{"reports.executive.read", "reports.sensitive.read"},
}
```

Capability keys must be valid dot-separated strings:

```text
valid:
  sales.create
  reports.sensitive.read

invalid:
  sales
  sales.*
  reports..read
  reports.sensitive.read.extra
```

Use exactly two or three parts:

```text
<domain>.<action>
<domain>.<resource>.<action>
```

## CapabilityProfile

`CapabilityProfile` is the proposed Dexco-level session recipe for product
agents. It combines behavior and runtime policy, but it does not replace the
host application's authorization system.

Proposed shape:

```go
type CapabilityProfile struct {
	ID                  string
	Description         string
	Principal           Principal
	Mode                ProfileMode
	Config              Config
	Handlers            []ProfileHandler
	RunnerOptions       RunnerOptions
	Guardrails          Guardrails
	ProgressNarration   ProgressNarrationConfig
	Metadata            map[string]string
}
```

`ProfileMode` values:

```go
type ProfileMode string

const (
	ProfileModeSingleRole    ProfileMode = "single_role"
	ProfileModeActiveRole    ProfileMode = "active_role"
	ProfileModeCombinedRoles ProfileMode = "combined_roles"
)
```

Field meanings:

- `ID`: stable profile identifier, such as `sales`, `management`, or
  `customer_support`.
- `Description`: human-readable purpose for logs, debugging, and docs.
- `Principal`: host-resolved user, roles, active role, and capabilities.
- `Mode`: how roles were resolved for this session.
- `Config`: Dexco session config, including base instructions and style.
- `Handlers`: tools visible to the model for this profile.
- `RunnerOptions`: retry, hooks, parallel tool runtime, and tool-output limits.
- `Guardrails`: approval policy and reviewer for pre-tool authorization.
- `ProgressNarration`: optional delayed user-facing progress events derived
  from active runtime state.
- `Metadata`: optional non-sensitive application facts for logging or routing.

In profile sessions, `CapabilityProfile.Guardrails` is the canonical guardrail
configuration. If the existing `RunnerOptions` type also contains a `Guardrails`
field, profile validation must reject a non-empty
`profile.RunnerOptions.Guardrails`. Profile construction then installs
`profile.Guardrails` into the runner so callers do not accidentally configure
two policy sources.

The profile is an input to session construction. It should be immutable for a
session unless the host explicitly creates a new session or applies a deliberate
profile update.

For v1, do not support in-place profile updates. Treat the profile as immutable
after session construction. A deliberate profile update means constructing a
new session.

## Public API surface

The first implementation should add only these profile-specific entry points:

```go
func NewSessionForProfile(
	ctx context.Context,
	modelClient ModelClient,
	profile CapabilityProfile,
) (*Session, error)

func ValidateCapabilityProfile(profile CapabilityProfile) error
```

`NewSessionForProfile` must:

```text
validate profile
defensively copy profile slices and maps
derive visible handlers from profile.Principal.Capabilities
build router from visible handlers
install profile-aware guardrail checks
install progress narration if enabled
create runner from profile.RunnerOptions
create session from profile.Config
attach profile/principal to handler, hook, and reviewer contexts
```

There should not be a `NewSessionForProfileWithOptions` in v1. The profile is
the single source of construction behavior. If callers need lower-level
composition, they should use:

```go
router, err := dexco.NewRouter(...)
runner, err := dexco.NewRunnerWithOptions(modelClient, router, options)
session, err := dexco.NewSession(cfg, runner)
```

## Profile validation

`ValidateCapabilityProfile` must reject profiles that are impossible to enforce
safely.

Required fields:

```text
profile.ID
profile.Principal.UserID
profile.Principal.TenantID
```

For single-tenant products, callers should use a stable tenant value such as
`default` rather than leaving `TenantID` empty.

Validation rules:

- `profile.ID` must be stable, non-empty, and safe for logs.
- `Principal.UserID` and `Principal.TenantID` must be non-empty.
- `Principal.Capabilities` must contain only valid exact capability keys.
- `Principal.Capabilities` must not contain wildcards.
- `Principal.Capabilities` should be de-duplicated by the host; Dexco may
  defensively de-duplicate while preserving behavior.
- `Principal.Roles` and `Principal.ActiveRole` are descriptive only and must
  not be used as authorization checks.
- `profile.Mode` must be one of `single_role`, `active_role`, or
  `combined_roles`.
- `single_role` is valid when `Principal.Roles` has zero or one effective role.
- `active_role` requires `Principal.ActiveRole` to be non-empty.
- `combined_roles` requires deterministic persona selection in
  `profile.Config`.
- `profile.Handlers` may be empty for a pure chat session.
- Handler names must be unique after visibility filtering.
- Handler specs must be valid model tool specs.
- `profile.Guardrails` is the only guardrail source for profile sessions.
- `profile.RunnerOptions.Guardrails` must be empty if that field exists on the
  reused `RunnerOptions` type.
- A nested guardrail config is non-empty when it sets a policy, reviewer,
  permission grant store, or other approval-related field.
- Profile metadata must be safe for logs and must not contain secrets.
- `ProgressNarration.InitialDelay` must be greater than zero when progress
  narration is enabled.
- `ProgressNarration.RepeatInterval` may be zero to disable repeats. If set,
  it must be greater than or equal to `InitialDelay`.

Zero-value rules:

- Empty `ProfileMode` is invalid. The host must choose the mode explicitly.
- Empty `CapabilitySetVersion` is allowed but discouraged; audited products
  should set it.
- Empty `Guardrails` means only the non-bypassable capability check applies.
- Empty `ProgressNarrationConfig` means progress narration is disabled.
- Empty `Metadata` is valid.

Validation should return actionable errors that identify the bad field, such as:

```text
validate capability profile: principal.capabilities[2]: wildcard not supported
validate capability profile: handlers: duplicate tool name "get_report"
```

## Router relationship

`Router` should remain the concrete tool registry and dispatcher.

`CapabilityProfile` may contain handlers, but it should not hide that handlers
become a router internally:

```text
profile.Handlers -> NewRouter(profile.Handlers...) -> Runner -> Session
```

This avoids inventing a second abstraction for "named router presets." If a
caller wants full control, they can still call `NewRouter` directly.

## Profile handlers

Do not change the low-level `Handler` contract just to support product
profiles. Low-level handlers should remain usable with `NewRouter` and
`NewRunnerWithOptions`.

Profile sessions should require additional metadata through a profile-specific
handler contract:

```go
type ProfileHandler interface {
	Handler
	Visibility(ctx context.Context, principal Principal) (CapabilityRequirement, error)
	RequiredCapabilities(ctx context.Context, call ToolCall) (CapabilityRequirement, error)
	Progress(ctx context.Context, call ToolCall) (ProgressHint, error)
}
```

Responsibilities:

- `Visibility` returns the coarse requirement used to decide whether the tool is
  visible to the model for this profile.
- `RequiredCapabilities` returns the final requirement for this exact tool call
  and arguments.
- `Progress` returns safe user-facing progress metadata after authorization and
  argument validation.

Concurrency and safety:

- `Visibility`, `RequiredCapabilities`, `Progress`, and `Call` may be invoked
  concurrently when parallel tools are enabled.
- `Visibility`, `RequiredCapabilities`, and `Progress` must be side-effect free.
- `Progress` must return a non-empty safe label for profile sessions.

Failure behavior:

- If `Visibility` returns an error, profile construction should fail.
- If `RequiredCapabilities` returns an error, Dexco must deny the call and not
  run the tool because the action cannot be classified safely.
- If `Progress` returns an error, Dexco should fall back to generic progress
  text and continue because progress text is not an authorization boundary.

Existing handlers should be adapted explicitly:

```go
type HandlerProfilePolicy struct {
	Visibility           CapabilityRequirement
	RequiredCapabilities CapabilityRequirement
	Progress             ProgressHint
}

func NewProfileHandler(handler Handler, policy HandlerProfilePolicy) ProfileHandler
```

Use the adapter for simple static tools. Implement `ProfileHandler` directly
when visibility, required capabilities, or progress details depend on
arguments or application state.

Profile sessions must not accept a bare `Handler` without profile metadata. If
a handler is usable in a profile, the implementation must know its visibility
requirement, exact-call capability requirement, and progress hint behavior.

## Explicit handler groups

Dexco may provide convenience functions that return handler slices. For profile
sessions, these helpers should return `[]ProfileHandler`:

```go
func ChatWorkflowProfileHandlers(responder UserInputResponder) []ProfileHandler
func CodingWorkflowProfileHandlers(responder UserInputResponder) []ProfileHandler
```

Naming rules:

- Do not use `Default*`; default is ambiguous.
- Do not use `LocalAutomation*`; it is vague about the actual agent shape.
- Do not use `ConversationProfile*`; it is wordy and does not clearly describe
  chatbot/product usage.
- Use `*Workflow*` for helper groups that compose tools for a workflow.
- Use `ChatWorkflow*` for chatbot-safe helpers.
- Use `CodingWorkflow*` for Codex-like coding-agent helpers.

Rules for new helper names:

- Name helpers by the user-facing intent they support, not by internal
  implementation details.
- Prefer short domain words over abstract platform words.
- Use `*Handlers` for lower-level `[]Handler` helpers.
- Use `*ProfileHandlers` for profile-aware `[]ProfileHandler` helpers.
- Place `Workflow` before `Handlers` or `ProfileHandlers`.
- Do not invent a new top-level noun unless it carries new policy semantics.
- Avoid names that imply a complete product policy when the helper only returns
  generic tools.

Examples:

```text
Good:
  ChatWorkflowProfileHandlers
  CodingWorkflowProfileHandlers
  SalesWorkflowProfileHandlers
  CustomerCareWorkflowProfileHandlers
  BackOfficeWorkflowProfileHandlers

Avoid:
  DefaultHandlers
  LocalAutomationHandlers
  ConversationProfileHandlers
  ChatProfileHandlers
  SalesProfileHandlers
  ToolsetHandlers
  StandardHandlers
  BasicHandlers
```

Handler group names do not grant roles and do not define authorization. A name
such as `SalesWorkflowProfileHandlers` means "tools commonly useful in the sales
workflow," not "this session has the sales role."

Authorization remains capability-driven:

```text
roles -> host-resolved capabilities
capabilities -> profile visibility and exact-call authorization
handler groups -> convenient tool composition for a workflow
```

`ChatWorkflowProfileHandlers` should include only low-side-effect helpers
appropriate for multi-user chat products, such as:

```text
current_time
request_user_input
```

`ChatWorkflow*` helpers are not a complete chatbot policy. They are only safe
generic helpers that product profiles can combine with application-domain
tools.

`CodingWorkflowProfileHandlers` should preserve the Codex-like coding-agent
bundle, such as:

```text
exec_command
current_time
request_user_input
update_plan
view_image
```

The host should add business-domain tools explicitly:

```text
create_sale
view_assigned_leads
get_sensitive_report
request_admin_handoff
```

The lower-level non-profile API may keep ordinary `[]Handler` helpers if useful,
but profile construction should use profile-aware helpers only.

## Request permissions

`request_permissions` is a Codex-style local automation tool. It should not be
included in `ChatWorkflowProfileHandlers` by default.

In profile sessions, permissions requested by the model must not add business
capabilities to `Principal.Capabilities`. The host is the only source of
capabilities.

If an application chooses to expose a permission-request workflow, model
requests may only ask for extra review or human handoff around an action the
principal is already allowed to perform. They must not convert a denied
capability check into an approved call.

## Migration from current Dexco API

Current Dexco exposes ambiguous helpers such as:

```text
DefaultHandlers
NewDefaultRouter
NewDefaultSession
NewDefaultSessionWithOptions
```

These names should be removed in the first implementation pass. Do not keep
compatibility wrappers. They are ambiguous because "default" can mean a local
coding agent, a chatbot agent, or a minimal test session.

Replacement naming:

```go
func CodingWorkflowHandlers(responder UserInputResponder) []Handler
func NewCodingWorkflowRouter(responder UserInputResponder) (*Router, error)
func NewCodingWorkflowSession(
	cfg Config,
	modelClient ModelClient,
	responder UserInputResponder,
) (*Session, error)
func NewCodingWorkflowSessionWithOptions(
	cfg Config,
	modelClient ModelClient,
	responder UserInputResponder,
	options RunnerOptions,
) (*Session, error)

func CodingWorkflowProfileHandlers(responder UserInputResponder) []ProfileHandler
func ChatWorkflowProfileHandlers(responder UserInputResponder) []ProfileHandler
```

Migration map:

```text
DefaultHandlers                  -> CodingWorkflowHandlers
NewDefaultRouter                 -> NewCodingWorkflowRouter
NewDefaultSession                -> NewCodingWorkflowSession
NewDefaultSessionWithOptions     -> NewCodingWorkflowSessionWithOptions
```

Construction replacements:

```go
// Codex-like coding workflow convenience construction.
session, err := dexco.NewCodingWorkflowSessionWithOptions(
	cfg,
	modelClient,
	responder,
	options,
)

// Product-agent construction.
session, err := dexco.NewSessionForProfile(ctx, modelClient, profile)
```

Strict migration:

```text
remove Default*
add CodingWorkflow*
add ChatWorkflow*
update README, examples, and tests to use explicit helpers
do not keep Default* compatibility wrappers
```

Do not introduce `Toolset` as a public concept. A named collection of handlers
is still just handler composition. The meaningful product-level abstraction is
`CapabilityProfile`.

## Tool visibility

Tool visibility is the first authorization boundary, but not the only one.

For each session, the profile should expose only the tools that are reasonable
for the principal and active role. A customer session should not see internal
management tools. A sales session should not see sensitive reporting tools
unless the active profile explicitly allows them.

Tool visibility should be derived from `ProfileHandler.Visibility` and
`Principal.Capabilities`:

```text
tool create_sale             requires sales.create
tool view_assigned_leads     requires sales.leads.read
tool get_sensitive_report    requires reports.sensitive.read
tool request_admin_handoff   requires handoff.admin.create
```

This mapping is not always one capability per tool. A tool can require one
capability, multiple capabilities, or different capabilities depending on the
call arguments.

Examples:

```text
tool create_sale:
  requires sales.create

tool get_report(report_type = executive):
  requires reports.executive.read

tool get_report(report_type = sensitive):
  requires reports.sensitive.read

tool approve_discount:
  requires discounts.approve
  requires customers.profile.read
```

Tool visibility should use a coarse capability check so the model only sees
reasonable tools. Final authorization must still evaluate the exact tool call
and its arguments.

Even if a tool is not visible, the tool handler must still enforce
authorization. Hidden tools reduce model mistakes; handler-side checks prevent
security bypass.

## Deferred tools

Deferred tools and tool search must obey the same profile visibility policy as
ordinary advertised tools.

For profile sessions:

- A tool that fails `ProfileHandler.Visibility` must not be advertised.
- A tool that fails `ProfileHandler.Visibility` must not appear in tool-search
  results.
- A tool that fails `ProfileHandler.Visibility` must not be callable by exact
  name.
- Final authorization still evaluates `RequiredCapabilities` for the exact call
  before dispatch.

## Guardrail enforcement

Guardrails are the second authorization boundary. Before a tool runs, Dexco
should ask the configured reviewer whether this exact principal can execute
this exact tool call with these exact arguments.

Profile-session ordering:

```text
model emits tool call
        |
        v
allowed hook mutation
        |
        v
capability classification and check on the final call
        |
        v
optional stricter reviewer
        |
        v
dispatch only if still approved
```

Hooks may normalize or reject a call, but they must not grant capabilities.
Reviewers may deny or require stricter handling, but they must not approve a
call that failed the capability check.

```text
final tool call after allowed hook mutation
        |
        v
ProfileHandler.RequiredCapabilities classifies the exact call
        |
        v
Dexco checks required capabilities against Principal.Capabilities
        |
        +--> missing capability: deny, do not run tool
        |
        v
handler or router classifies risk through ToolGuardrail
        |
        v
optional reviewer applies stricter product policy
        |
        +--> denied: return safe tool result, emit decision event, do not run tool
        |
        v
approved: run tool
```

This approval can be fully automatic. For product RBAC, approval means "policy
allowed this call." It does not imply a human must click approve.

Capability checks are non-bypassable in profile sessions. A custom reviewer may
deny an otherwise allowed call, but it must not approve a call when the
principal is missing a required capability.

Decision semantics:

```text
approved:
  required capabilities are present and no reviewer denied the call

denied:
  required capabilities are missing, the handler classified the call as denied,
  or the reviewer denied the call

no_decision:
  only valid inside reviewer chaining; it must not be exposed as the final
  profile-session decision
```

Denied tool calls must produce:

```text
safe model-visible tool result:
  "This action is not allowed for the current user."

client event:
  type: tool_approval_decision
  decision: denied
  tool name
  turn ID
  safe reason code
```

Proposed event payload:

```go
type ToolPolicyDecision struct {
	ToolName             string
	Decision             ApprovalDecision
	ReasonCode           string
	RequiredCapabilities CapabilityRequirement
}
```

`ClientEvent` should carry `ToolPolicyDecision *ToolPolicyDecision` when
emitting `ClientEventToolApprovalDecision` for profile-session policy decisions.
The existing `ApprovalDecision` field remains the summary decision for clients
that do not inspect the profile-specific payload.

Reason codes should be stable and non-sensitive:

```text
missing_capability
policy_denied
manual_review_required
invalid_scope
handler_denied
```

Manual review is still useful for exceptional workflows:

- Refund above a threshold.
- Discount above a threshold.
- Bulk email or external notification.
- Admin handoff requiring a human operator.
- Access to unusually sensitive records.

In v1, manual review should be modeled as a denial with reason code
`manual_review_required`, plus an application-owned handoff/ticket workflow.
Dexco should not block waiting for a human unless a future API explicitly adds
that behavior.

## Handler authorization

Tool handlers must re-check authorization against trusted application state.

This is required because:

- A model-visible tool list is not a security boundary.
- A guardrail reviewer can be misconfigured.
- Tool arguments can target records outside the user's tenant or scope.
- Authorization may depend on current database state.

Example rule:

```text
create_sale requires:
  principal has sales.create
  sale.tenant_id == principal.tenant_id
  sale.owner_id is allowed by sales territory policy
```

Dexco should help pass principal/profile context to handlers, but the handler
or application service must enforce the final business rule.

## Active work and progress narration

Dexco should always know the current observable work, but it should not always
emit that work to the client.

```text
internal ActiveWork state:
  updated immediately

client ProgressNarration event:
  emitted only if enabled and the current work takes long enough
```

This avoids guessing. Dexco should not infer what the model is thinking from
silence. Instead, every long-running subsystem updates an explicit active-work
state:

```text
runner          -> waiting for model, generating reply
guardrails      -> checking policy
retry policy    -> retrying or backing off
tool runtime    -> running one tool
parallel runtime -> waiting for multiple tools
handler         -> domain-specific operation label and safe detail
```

Proposed shape:

```go
type WorkPhase string

const (
	WorkPhaseWaitingForModel WorkPhase = "waiting_for_model"
	WorkPhaseCheckingPolicy  WorkPhase = "checking_policy"
	WorkPhaseRunningTool     WorkPhase = "running_tool"
	WorkPhaseRetryingTool    WorkPhase = "retrying_tool"
	WorkPhaseWaitingParallel WorkPhase = "waiting_parallel_tools"
	WorkPhaseGeneratingReply WorkPhase = "generating_reply"
)

type ActiveWork struct {
	Phase     WorkPhase
	ToolName  string
	Label     string
	Detail    string
	StartedAt time.Time
}

type ProgressNarrationConfig struct {
	Enabled         bool
	InitialDelay    time.Duration
	RepeatInterval  time.Duration
}
```

`ActiveWork` is the source of truth. `ProgressNarrationConfig` only controls
whether and when active work becomes a client event.

Recommended defaults:

```text
Enabled: false
InitialDelay: application-chosen, for example 800ms
RepeatInterval: disabled unless the host explicitly wants repeated updates
```

Emission rules:

```text
set ActiveWork immediately
do not emit immediately
emit only if the same work is still active after InitialDelay
reset the delay when active work changes
suppress narration if visible assistant output arrives first
clear or hide narration when normal output resumes or the turn completes
```

Examples:

```text
policy check finishes in 30ms:
  emit nothing

reports.sensitive.read runs longer than InitialDelay:
  emit "Reading Q2 revenue summary"

model has not emitted tokens or tool calls yet:
  emit "Waiting for model"
```

The last case is intentionally generic. Dexco can accurately report that it is
waiting for the model, but it cannot know the model's private reasoning.

Client event shape:

```go
const ClientEventProgressNarration ClientEventType = "progress_narration"

type ProgressNarration struct {
	Phase     WorkPhase
	Message   string
	Label     string
	Detail    string
	ToolName  string
	StartedAt time.Time
	Elapsed   time.Duration
}
```

`ClientEvent` should carry `ProgressNarration *ProgressNarration` when
`Type == ClientEventProgressNarration`.

Payload rules:

- `Message` is the final display string, such as `Reading Q2 revenue summary`.
- `Label` is required when the event describes a tool.
- `Detail` is optional.
- `ToolName` is allowed for client routing and debugging, but clients should
  display `Message`.
- Do not include raw tool arguments, raw tool output, hidden reasoning, or
  sensitive policy internals.
- `Message` should be short enough for a status line. Target 3-7 words.
- `Message`, `Label`, and `Detail` must have hard length caps before emission.

Recommended caps:

```text
Message: 96 characters
Label: 48 characters
Detail: 64 characters
```

Lifecycle rules:

- Turn completion clears active work and cancels pending narration timers.
- Turn cancellation clears active work and cancels pending narration timers.
- Tool completion, failure, or denial clears that tool's active work.
- Dexco must not emit progress narration after the turn has completed.
- JSON or RPC adapters must define stable time and duration encoding before
  exposing progress events over a wire protocol.

## Parallel progress narration

When `RunnerOptions.ParallelTools` is enabled and multiple tools are running,
Dexco should avoid per-tool progress spam.

Default parallel behavior:

```text
one active slow tool:
  emit that tool's safe progress message

multiple active slow tools:
  emit one aggregate message

aggregate message:
  "Running 2 tasks"
  "Fetching account data"
  "Checking multiple items"
```

If all active tools share the same safe label, Dexco may use that label:

```text
Reading reports
```

If active tools have different labels, Dexco should prefer a generic aggregate
message over rapidly switching messages.

Progress narration must reset when the parallel group changes materially:

```text
first tool starts
second tool joins group
one tool finishes and one remains
all tools finish
```

Repeats remain disabled unless `RepeatInterval` is configured.

## Handler progress hints

Every `ProfileHandler` must provide a progress hint. The tool owns what is
happening, but it must not decide when to emit client progress events.

```text
handler owns:    what is happening
runner owns:     current ActiveWork
narrator owns:   delayed client emission
```

Proposed shape:

```go
type ProgressHint struct {
	Label  string
	Detail string
}
```

Simple handlers can return a generic but accurate label. Domain handlers should
return a useful safe detail when available:

```text
reports.sensitive.read -> Label: "Reading report", Detail: "Q2 revenue summary"
sales.order.create     -> Label: "Creating sale", Detail: "Acme renewal"
handoff.admin.create   -> Label: "Contacting admin", Detail: "billing support"
```

Progress details must be safe:

- Do not reveal sensitive tool arguments before policy approval.
- Do not blindly echo model-provided JSON into progress text.
- Prefer details derived from trusted application data after argument
  validation.
- If a detail is sensitive or unavailable, return only a generic label.
- `Progress` must be side-effect free.
- `Progress` must not authorize or deny the call.

If `Progress` returns an error, Dexco should use a generic safe fallback and
continue to the tool call unless the context is canceled:

```text
fallback label:
  "Running tool"
  "Fetching data"
  "Working on it"
```

An empty progress label should be treated the same as an unusable progress
hint: Dexco falls back to generic safe text.

Safe sequence:

```text
model emits tool call
        |
        v
Dexco sets ActiveWork to "Checking access"
        |
        v
profile policy approves exact call
        |
        v
handler resolves side-effect-free safe ProgressHint
        |
        v
Dexco sets ActiveWork to "Reading Q2 revenue summary"
        |
        v
narrator emits only if the tool is still running after InitialDelay
```

## Multi-role users

A user can have multiple roles:

```text
roles:
  management
  sales

capabilities:
  sales.create
  sales.leads.read
  reports.sensitive.read
  discounts.approve
```

Dexco should not choose roles by guessing from conversation text. The host
application should resolve one of these modes:

```text
ProfileModeCombinedRoles:
  capabilities are the union of assigned roles
  persona is selected by deterministic priority

ProfileModeActiveRole:
  capabilities are narrowed to the chosen active role
  persona follows the active role

ProfileModeSingleRole:
  one role or role-less service account
  capabilities are already resolved by the host
```

Recommended default for business chatbots:

```text
Use ProfileModeActiveRole when the UI/product has a clear selected role.
Use ProfileModeCombinedRoles only when the product intentionally wants
cross-role work.
```

When combining roles, use capability union for allowed actions, but avoid
blending personas into an inconsistent tone. Select one primary persona by
explicit priority or host-provided active role.

Example priority:

```text
management > sales > support > customer
```

A management+sales user in combined mode can perform both management and sales
actions, but the persona should be deterministic:

```text
persona: management, with sales workflow awareness
```

## Persona and tone

Persona is prompt behavior, not authorization.

The profile should use `Config.Instructions` and style/collaboration
instructions to describe how the assistant should behave:

```text
customer:
  simple, helpful, low jargon, escalate internal actions through handoff

sales:
  concise, CRM-oriented, action-focused, confirm customer/account details

management:
  strategic, direct, summary-first, include risk and operational impact
```

The model may use persona to choose wording and workflow style. It must not use
persona to decide whether a tool is allowed.

## Prompt composition

Profile data is not automatically model-visible.

Model-visible prompt content should include:

- `Config.Instructions` and persona/tone guidance.
- Tool specs for tools visible to the profile.
- Non-sensitive workflow guidance the host intentionally includes.

Model-visible prompt content should not include by default:

- Raw role assignment data.
- Raw capability lists.
- Policy internals or denial rules.
- Secrets, tokens, tenant-private metadata, or audit-only metadata.

If the assistant needs to explain what it can do, prefer user-facing capability
summaries such as `I can help create sales and view assigned leads` instead of
raw capability keys.

## Handoff

Handoff is an application-domain workflow. It should be represented as an
explicit application tool or application client event, not an implicit model
promise and not a special Dexco primitive.

Examples:

```text
request_admin_handoff
create_support_ticket
assign_ticket_to_admin
```

Capabilities:

```text
handoff.admin.create
tickets.create
tickets.assign
```

A customer can usually create a handoff request. Only staff or admins should be
able to assign or resolve the handoff.

## Profile resolution

The host application owns profile resolution.

Input:

```text
authenticated user
tenant
assigned roles
optional active role
product surface
conversation type
```

Output:

```text
CapabilityProfile
```

Dexco may define resolver-facing request/response types for convenience, but
the application owns the implementation:

```go
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
```

This interface is optional convenience. Applications can also construct
`CapabilityProfile` directly.

Profile resolution should be deterministic and auditable. The host should log
the selected profile ID, active role, capability set version, and visible tool
names.

## Audit and logging contract

Dexco should not write application audit logs directly. Dexco is a library; the
host owns log routing, retention, privacy filtering, and compliance.

Dexco should provide enough structured data for the host to audit:

```text
profile_selected:
  profile ID
  user ID
  tenant ID
  roles
  active role
  capability set version
  visible tool names

tool_policy_decision:
  turn ID
  tool name
  required capabilities
  decision
  reason code

tool_lifecycle:
  turn ID
  tool name
  started/completed/failed
  duration

progress_narration:
  turn ID
  phase
  message
```

Recommended delivery:

- Host logs `profile_selected` before calling `NewSessionForProfile`.
- Dexco emits tool approval request/decision events through the existing client
  event stream.
- Dexco emits tool lifecycle through the existing lifecycle hook.
- Dexco emits progress narration through `ClientEventProgressNarration` only
  when enabled.
- Handlers log domain-specific audit records in the application service layer
  when they perform side effects.

Audit payloads must not include hidden model reasoning, raw sensitive tool
arguments, raw secrets, or full tool outputs.

## Session isolation

Use separate Dexco sessions for separate users and conversations.

Do not reuse a management session history for a customer session. Conversation
history can contain sensitive tool outputs or contextual instructions. A role
or profile downgrade should create a new session or apply a carefully designed
history redaction process before continuing.

Recommended rule:

```text
Different user -> different session.
Different tenant -> different session.
Privilege downgrade -> new session unless history is sanitized.
Privilege upgrade -> new session when sensitive context may be introduced.
```

## Proposed construction flow

```go
principal := auth.ResolvePrincipal(request)
profile := profiles.Resolve(principal, request.ActiveRole)

session, err := dexco.NewSessionForProfile(ctx, modelClient, profile)
```

Internally:

```text
validate profile
copy profile
filter ProfileHandlers by Visibility against Principal.Capabilities
build router from visible ProfileHandlers
install non-bypassable capability checks
install profile.Guardrails as stricter policy
install progress narrator if enabled
apply profile policy after allowed hook mutation
copy profile.RunnerOptions and reject nested guardrails
create runner
create session from profile.Config
attach profile/principal to per-call contexts
```

## Example profiles

Customer:

```text
ID: customer
capabilities:
  tickets.create
  handoff.admin.create
visible tools:
  current_time
  request_user_input
  create_support_ticket
  request_admin_handoff
persona:
  helpful, simple, no internal jargon
```

Sales:

```text
ID: sales
capabilities:
  sales.create
  sales.leads.read
  customers.profile.read
  handoff.admin.create
visible tools:
  current_time
  request_user_input
  create_sale
  view_assigned_leads
  request_admin_handoff
persona:
  concise, CRM-oriented, action-focused
```

Management:

```text
ID: management
capabilities:
  reports.sensitive.read
  reports.executive.read
  discounts.approve
  sales.leads.read
visible tools:
  current_time
  request_user_input
  get_sensitive_report
  get_executive_report
  approve_discount
persona:
  strategic, direct, summary-first
```

Management plus sales in active-role mode:

```text
assigned roles:
  management
  sales
active role:
  sales
effective capabilities:
  sales.create
  sales.leads.read
  customers.profile.read
persona:
  sales
```

Management plus sales in combined mode:

```text
assigned roles:
  management
  sales
effective capabilities:
  sales.create
  sales.leads.read
  customers.profile.read
  reports.sensitive.read
  discounts.approve
persona:
  management, with sales workflow awareness
```

## Testing plan

Implementation must include tests for these behaviors:

- `ValidateCapabilityProfile` rejects missing profile ID, user ID, tenant ID,
  invalid capability keys, wildcard capabilities, invalid profile mode, and
  duplicate visible tool names.
- Profile construction exposes only handlers whose `Visibility` requirement is
  satisfied by `Principal.Capabilities`.
- A hidden tool is not advertised to the model.
- A hidden or unregistered tool call does not dispatch.
- Hidden deferred tools are not advertised, searchable, or callable.
- `RequiredCapabilities` is evaluated per exact tool call and arguments.
- Capability checks evaluate the final tool call after allowed hook mutation.
- A call with missing required capabilities is denied before handler dispatch.
- A custom reviewer can deny an otherwise allowed call.
- A custom reviewer cannot approve a call with missing capabilities.
- `request_permissions` cannot add business capabilities in profile sessions.
- Raw roles, capability lists, policy internals, and metadata are not injected
  into the prompt by default.
- Denied calls emit a safe tool result and a structured client decision event.
- Handler-side authorization errors are returned as normal tool failures and do
  not bypass profile policy.
- `ProfileModeActiveRole` requires `Principal.ActiveRole`.
- `ProfileModeCombinedRoles` preserves unioned capabilities but deterministic
  persona instructions.
- Progress narration is not emitted before `InitialDelay`.
- Progress narration is suppressed when work completes before `InitialDelay`.
- Progress narration emits safe handler-provided text for slow tools.
- Progress narration falls back to generic text when `Progress` errors.
- Progress narration is cleared on turn completion, cancellation, tool failure,
  and tool denial.
- Parallel tools produce one aggregate progress event by default.
- Profile/principal context helpers return the expected values inside handlers,
  hooks, and reviewers.
- `NewSessionForProfile` defensively copies slices and maps so later caller
  mutation does not change an active session.
- `Default*` helpers are removed, not wrapped.
- `CodingWorkflow*` constructors cover the former Codex-like `Default*`
  workflow.
- Examples, README, and tests use explicit workflow helpers.

## Relation to Codex

Original Codex does not have one exact `CapabilityProfile` object. It splits
similar concerns across:

- config profiles
- permission profiles
- approval policy and approval reviewer
- custom agents
- MCP tool filters
- developer instructions and personality/style settings
- managed requirements

Dexco's `CapabilityProfile` should adapt those ideas into one library-level
session recipe for product agents. Keep comments near the implementation that
explain which Codex concept each piece maps to, so future Codex improvements
can be adopted deliberately.

## Future considerations

These are intentionally out of scope for v1:

- Wildcard capabilities such as `reports.*.read`.
- Mid-session profile mutation.
- Blocking human approval workflows inside Dexco.
- Dexco-owned authentication or role lookup.
- Dexco-owned application audit logging.
- A separate `Toolset` abstraction.
- Context-history redaction for privilege downgrade.

If any of these become necessary, update this document first and add tests that
cover the new behavior.
