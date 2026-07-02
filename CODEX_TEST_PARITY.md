# Codex Test Parity

This file tracks how Rust Codex tests map into Dexco. Dexco is a Go library
extraction of the non-realtime LLM loop, so the useful parity target is the
loop/tool/event contract, not Codex app-server, TUI, cloud, sandbox, or storage
infrastructure.

When Codex adds or changes tests in a portable area, update this map and add a
Dexco test with a comment naming the Codex behavior it preserves.

## Adopted

- `core/tests/suite/stream_no_completed.rs`
  - Dexco: `TestRunnerRetriesEarlyEOFBeforeCompleted`
  - Invariant: stream close before `completed` is retryable; partial output is not visible or committed.

- `core/tests/suite/stream_error_allows_next_turn.rs`
  - Dexco: `TestSessionCanContinueAfterFailedTurnWithoutPersistingFailedInput`
  - Invariant: a failed turn does not poison the session or persist failed input.

- `core/tests/suite/abort_tasks.rs`
  - Dexco: `TestRunnerCancelsRunningToolWithoutCompletingTurn`, `TestSessionPersistsAbortedToolTranscriptForNextTurn`
  - Invariant: cancellation while a tool is running aborts the turn instead of committing a completed turn event, carries the attempted tool call plus a synthetic aborted tool output on the error, and persists a `<turn_aborted>` marker for the next model request.

- `core/tests/suite/client.rs` and `core/src/session/tests.rs`
  - Dexco: `TestSessionPassesConfiguredInstructionsToModelPrompt`, `TestSessionEmitsTurnStartedBeforeSampling`, `TestRunnerDoesNotDuplicateAssistantMessageWhenFinalItemFollowsDeltas`, `TestRunnerUsesOutputItemAddedAssistantText`
  - Invariant: prompt instructions stay out of history, turn-start events precede blocking sampling work, and assistant stream assembly avoids duplicate model-visible messages.

- `core/tests/suite/turn_state.rs`
  - Dexco: `TestSessionTurnStatePersistsWithinTurnAndResetsAfter`, `TestSessionTurnStateIsStableWithinTurn`
  - Invariant: provider-supplied turn state is prompt metadata, not history; the first value seen in a turn is replayed on later same-turn sampling requests, later provider values cannot overwrite it, and the next user turn starts without the previous token.

- `core/tests/suite/json_result.rs` and output-schema API coverage in `app-server/tests/suite/v2/output_schema.rs`
  - Dexco: `TestSessionForwardsOutputSchemaToTurnPrompts`
  - Invariant: final-output JSON schema is prompt metadata for every sampling request in that turn, does not become durable history, does not leak into later turns when omitted, and JSON assistant text is surfaced unchanged.

- `core/tests/suite/prompt_caching.rs`
  - Dexco: `TestSessionPromptToolSpecsStableAcrossFollowUpRequests`
  - Invariant: provider cache headers are out of Dexco's scope, but Dexco-owned prompt construction keeps `Prompt.Tools` stable across the initial sampling request and follow-up requests after tool results are appended to history.

- `core/tests/suite/current_time_reminder.rs`
  - Dexco: `TestSessionInjectsCurrentTimeReminders`, `TestSessionCurrentTimeReminderZeroIntervalAllowsBackwardTime`, `TestSessionCurrentTimeReminderFailureStopsBeforeSampling`
  - Invariant: current-time reminders are developer prompt fragments, persist across same-turn follow-up requests, obey interval/zero-interval behavior, and clock failures stop before sampling.

- `core/tests/suite/view_image.rs` and `core/src/image_preparation_tests.rs`
  - Dexco: `TestCodingWorkflowSessionRunsViewImageToolRoundTrip`, `TestSessionPreparesUserTurnLocalImageParts`, `TestSessionUserTurnInvalidLocalImageBecomesPlaceholder`, `TestUserInputLocalImageHighDetailResizes`, `TestUserInputLocalImageOriginalDetailUsesOriginalBudget`, `TestUserInputDataURLSmallImagePreservesBytesAndRemoteURLBecomesPlaceholder`, `TestUserInputImageDetailPoliciesMatchCodexBudgets`, `TestUserInputLocalImageFailuresBecomePlaceholders`, `TestViewImageHandlerAttachesResizedHighDetailImages`, `TestViewImageHandlerOriginalDetailPreservesDimensions`, `TestViewImageHandlerDetailValidation`, `TestViewImageHandlerGuardrailIsReadOnly`
  - Invariant: `view_image` attaches local images as model-visible image content, user-turn local images become prompt content parts with Codex image budgets, invalid local images become bounded placeholders rather than failed turns, high/auto/omitted detail is resized/bounded, small data-url images are preserved without byte churn, remote image URLs become bounded placeholders, original detail preserves the Codex larger budget, low/invalid detail returns clear placeholder or failed text, null detail behaves like omitted high detail, and the tool is read-only.

- `core/tests/suite/request_user_input.rs`, `core/src/tools/handlers/request_user_input_spec_tests.rs`, and portable cases from `core/src/tools/handlers/request_user_input_tests.rs`
  - Dexco: `TestCodingWorkflowSessionRunsRequestUserInputRoundTrip`, `TestCodingWorkflowSessionRunsStructuredRequestUserInputRoundTrip`, `TestRequestUserInputHandlerSpecIncludesStructuredQuestionFields`, `TestRequestUserInputHandlerUsesCodexStructuredQuestionsWithSimpleResponder`, `TestRequestUserInputHandlerUsesStructuredResponder`, `TestRequestUserInputHandlerNormalizesAutoResolutionMSForStructuredResponder`, `TestRequestUserInputHandlerEnablesImplicitOtherOptionAndPreservesSecret`, `TestRequestUserInputHandlerRejectsStructuredQuestionWithoutOptions`
  - Invariant: request-user-input resolves to a tool result and sampling resumes; Codex-style structured question payloads produce Codex-compatible JSON answer maps, require non-empty model-supplied options, preserve client metadata such as `isSecret`, normalize `isOther=true` for the client-owned free-form Other option, and bound auto-resolution hints.

- `core/src/tools/handlers/plan.rs`, `core/src/tools/handlers/plan_spec.rs`, and update-plan coverage in `core/tests/suite/tool_harness.rs` / `core/tests/suite/tools.rs`
  - Dexco: `TestCodingWorkflowSessionRunsUpdatePlanRoundTrip`, `TestCodingWorkflowSessionRejectsMalformedUpdatePlanWithoutPlanEvent`, `TestUpdatePlanHandlerReturnsPlanUpdateMetadata`, `TestUpdatePlanHandlerRejectsMalformedPayload`, `TestUpdatePlanHandlerSpecAndGuardrail`, `TestCodingWorkflowRouterExposesBuiltinToolSpecs`
  - Invariant: `update_plan` is a coding-workflow direct utility tool, exposes the Codex checklist schema with required plan items and `pending`/`in_progress`/`completed` statuses, returns the fixed model-visible output `Plan updated`, emits a structured plan-update client event only for valid payloads, rejects malformed payloads as failed model-visible tool results, and classifies as read-only/no-approval.

- `core/tests/suite/additional_context.rs`
  - Dexco: `TestSessionAdditionalContextIsModelVisibleButNotUserHistory`, `TestSessionAdditionalContextDedupesWhileRetainingModelContext`, `TestSessionAdditionalContextValuesAreTruncated`
  - Invariant: per-turn additional context is model-visible without becoming a user-message item, trusted application context uses developer role while untrusted context uses user-role `external_` tags, unchanged active context is deduped while retained in model context, re-added context is visible again, and large values are truncated before model input.

- `core/tests/suite/tool_parallelism.rs`
  - Dexco: `TestRunnerCanDispatchParallelSafeToolsConcurrently`, `TestRunnerGroupsToolResultsAfterAllToolCalls`, `TestRunnerStartsToolsBeforeResponseCompleted`, `TestRunnerRetriesReceiveFailureWithStartedToolTranscript`
  - Invariant: parallel-safe tools may overlap, model-visible outputs stay ordered after all calls, completed tool-call items can start execution before `response.completed`, and retry prompts preserve already-started tool call/output transcripts without flushing failed-attempt text deltas.

- `core/src/context_manager/history_tests.rs` function/custom-tool output truncation coverage, plus double-truncation coverage in `core/tests/suite/truncation.rs` and `core/tests/suite/user_shell_cmd.rs`
  - Dexco: `TestTruncateToolResultOutputBoundsTextAndPreservesMetadata`, `TestTruncateToolResultOutputDoesNotDoubleTruncate`, `TestRunnerTruncatesToolResultOutputBeforeFollowUpPrompt`
  - Invariant: arbitrary tool-result text is bounded before it becomes future model-visible history, truncation keeps explicit markers plus useful head/tail text, already-truncated tool output is not wrapped in another truncation warning, and the bounded output preserves tool call identity, tool name, success state, plan metadata until the runner strips it for model replay, and structured non-text parts such as images and encrypted content.

- Dynamic-tool remote-image response coverage in `app-server/src/dynamic_tools.rs` and `app-server/tests/suite/v2/dynamic_tools.rs`
  - Dexco: `TestToolResultFromContentPartsRemoteImageURLBecomesTextError`, `TestToolResultWithoutRemoteImageURLsRejectsOnlyRemoteImageOutputs`
  - Invariant: rich tool-output image parts must be inline artifacts. `http:`/`https:` image URLs in dynamic/tool-result output are converted to a failed model-visible text result with Codex's remote-image error, while inline data URLs and raw image bytes remain valid.

- `core/tests/suite/items.rs`
  - Dexco: `TestRunnerCommitsCompletedReasoningItemWithoutDuplicate`, `TestRunnerSynthesizesReasoningItemFromDeltas`, `TestRunnerReasoningClientEventCarriesItemID`, `TestRunnerTextDeltaClientEventCarriesItemID`, `TestRunnerCommitsImageGenerationItemsAndEmitsClientEvents`
  - Invariant: reasoning items/deltas are emitted and committed without duplicate synthesized history, reasoning and assistant text client-event deltas preserve provider item IDs so streaming clients can reconcile them with completed history, text deltas inherit the active assistant item ID from `output_item.added` when the delta event omits it, and hosted image-generation calls are committed as first-class history and emitted to clients with status, revised prompt, result, and optional saved-path metadata even when artifact persistence is unavailable.

- `core/src/event_mapping_tests.rs` and `core/src/context/contextual_user_message_tests.rs`
  - Dexco: `TestRunnerCommitsWebSearchItemsAndEmitsClientEvents`, `TestRunnerCommitsImageGenerationItemsAndEmitsClientEvents`, `TestRunnerCommitsHookPromptItemAndEmitsClientEvent`, `TestParseHookPromptPartsHidesOtherContextualFragments`, `TestVisibleUserInputItemSkipsContextualOnlyInput`, `TestVisibleUserInputItemRejectsOnlyRegisteredContextualWrappers`, `TestVisibleUserInputItemSkipsMixedContextualInput`, `TestVisibleUserInputItemKeepsOrdinaryUserText`, `TestVisibleUserInputItemParsesTextAndTwoImages`, `TestVisibleUserInputItemSkipsLocalImageLabelText`, `TestVisibleUserInputItemSkipsUnnamedImageLabelText`, `TestAssistantMessageItemFromPartsAcceptsTextParts`, `TestContextualDeveloperContentRecognizesCodexPrefixes`, `TestContextualDeveloperContentDetectsMixedPersistentDeveloperText`, `TestContextualDeveloperContentIgnoresNonTextParts`
  - Invariant: hosted web-search actions (`search`, `open_page`, `find_in_page`, and partial/other) and hosted image-generation calls are preserved as first-class model-visible history items and surfaced as client events; hook prompt fragments are distinct visible turn items while surrounding contextual user fragments remain hidden from visible user-message history; registered contextual user wrappers including environment, AGENTS.md, skills, shell transcripts, abort markers, subagent notifications, recommended plugins, legacy goal context, internal model context, and additional context are hidden before they can become ordinary history while arbitrary context-like tags remain visible; multimodal user messages preserve text/images while dropping Codex's synthetic adjacent image label text; assistant text is accepted from legacy input-text payloads; contextual developer fragments are classified with Codex's prefix set and can be distinguished from persistent developer text.

- `core/tests/suite/web_search.rs` and `core/src/tools/hosted_spec_tests.rs`
  - Dexco: `TestSessionWebSearchModesResolvePromptAccess`, `TestSessionForwardsWebSearchConfigAndTurnOverride`
  - Invariant: hosted web-search request metadata is prompt metadata, cached mode disables external web access, live mode enables it, indexed mode enables external plus index-gated access, disabled mode omits web-search request metadata, configured search context size / allowed domains / approximate location are forwarded, and per-turn overrides can change the next prompt without becoming durable history.

- `protocol/src/models.rs` function-call output payload and MCP content conversion tests, plus portable helper coverage in `core/src/tools/context_tests.rs`, `core/src/client_common_tests.rs`, and `core/src/mcp_tool_call_tests.rs`
  - Dexco: `TestToolResultFromContentPartsPreservesMixedContent`, `TestNormalizeToolResultPartsPreservesDataURLAndDefaultsImageDetail`, `TestToolResultPartsTextIgnoresBlankTextImagesAndEncryptedContent`, `TestToolResultWithoutImageInputRewritesImageParts`, `TestItemsWithoutImageDetailStripsCopiesAndPreservesOriginal`, `TestToolResultWithWallTimePrefixesTextOutput`, `TestToolResultWithWallTimePreservesRichContentParts`, `TestToolTelemetryPreviewMatchesCodexLimits`
  - Invariant: structured tool outputs can preserve mixed text, image, and encrypted content; MCP-style image `data` plus `mimeType` is normalized into data URLs; existing data URLs are preserved; missing or unsupported image detail defaults to `high`; `original` detail and encrypted opaque payloads are preserved; legacy text surfaces use Codex's lossy newline-joined text-only fallback while ignoring blank text, images, and encrypted content; provider request copies can strip image detail from user/tool-result content without mutating durable history; provider request copies targeting models without image input rewrite image parts to Codex's text placeholder; wall-time envelopes can prefix both plain-text and rich-content tool results without flattening images; and telemetry previews are bounded to Codex's 2 KiB / 64-line caps with a fixed truncation notice and UTF-8 boundary safety.

- `core/src/stream_events_utils_tests.rs`, assistant-message stream parser tests in `core/src/session/tests.rs`, memory citation parser tests, and citation coverage in `core/tests/suite/items.rs`
  - Dexco: `TestAssistantTextParserStripsCitationsAcrossChunks`, `TestStripMemoryCitationsParsesStructuredCitation`, `TestParseMemoryCitationSupportsThreadIDsAndDedupes`, `TestAssistantTextParserPreservesPartialOpenTagAtFinish`, `TestAssistantTextParserAutoClosesUnterminatedCitation`, `TestRunnerStripsMemoryCitationsAcrossAssistantStream`, `TestRunnerCitationOnlyAssistantMessageHasNoVisibleFinalMessage`
  - Invariant: hidden `<oai-mem-citation>` blocks are removed from assistant visible text, final messages, text-delta client events, and committed history; citation markup can be split across `output_item.added` and text deltas; unterminated citations auto-close at stream finish; partial non-tag prefixes remain visible; structured citation entries and rollout/thread IDs are parsed into assistant-item metadata; duplicate rollout/thread IDs are removed; citation-only assistant messages do not become visible final assistant text.

- `core/tests/suite/safety_buffering.rs`, `core/tests/suite/models_etag_responses.rs`, and response metadata event tests
  - Dexco: `TestLibrarySinkCanObserveRawResponseEvents`
  - Invariant: newer response metadata events are preserved for library clients instead of rejected as unknown stream input.

- `core/src/turn_timing_tests.rs`
  - Dexco: `TestRunnerMetricsRecordsFirstOutputAndMessageOnce`, `TestRunnerMetricsClassifiesToolCallAsFirstOutputNotFirstMessage`, `TestRunnerMetricsIgnoresEmptyMessagesAndToolResultsForFirstOutput`, `TestRunnerMetricsProfileCountsSamplingRequestsAndRetries`
  - Invariant: turn telemetry records the turn start time, first observable model output only once, first assistant message independently, model actions such as tool calls as first output but not first message, ignores empty assistant messages and tool-result items for first-output timing, and profiles sampling attempts plus retry counts without changing prompt/history semantics.

- `core/tests/suite/rollout_budget.rs` and `core/src/context/rollout_budget.rs`
  - Dexco: `TestSessionRolloutBudgetAddsInitialAndThresholdReminders`, `TestSessionRolloutBudgetExhaustionFailsCurrentAndLaterTurns`
  - Invariant: rollout-budget reminders are bounded developer context wrapped in Codex's `<rollout_budget>` markers, the initial remaining budget is visible before sampling, completed response usage is charged as `output_tokens * sampling_weight + non_cached_input * prefill_weight`, crossed remaining-token thresholds append a fresh reminder without rewriting history, exhausted budgets return a typed error for the current and later turns, and later turns do not sample once the session budget is exhausted.

- `core/src/context/world_state/environment_render_tests.rs` and `core/src/context/world_state/environment_tests.rs`
  - Dexco: `TestSessionEnvironmentContextSnapshotRendersPortableCodexShape`, `TestSessionEnvironmentContextSingleEnvironmentIgnoresShellBecomingKnown`, `TestSessionEnvironmentContextDateTimezoneOnly`, `TestSessionEnvironmentContextUpdatesAndSortsMultipleEnvironments`, `TestSessionEnvironmentContextRendersEnvironmentStatuses`, `TestSessionEnvironmentContextSingleStartingUsesEnvironmentWrapper`
  - Invariant: embedders can provide Codex-shaped environment context without Dexco discovering environments itself; single available environments render legacy top-level cwd/shell fields, date/timezone-only context remains valid, multiple environments are sorted by ID under `<environments>`, status-bearing environments stay in explicit environment wrappers, unavailable environments render as self-closing `status="unavailable"` entries, subagent summaries render under `<subagents>`, unchanged snapshots do not duplicate model-visible context, single-environment shell discovery from unknown to known is treated as a no-op context diff while advancing the comparison baseline, and changed snapshots append a fresh contextual user fragment.

- `core/tests/suite/subagent_notifications.rs`, `core/src/context/subagent_notification.rs`, and bounded inter-agent completion coverage in `core/src/session_prefix_tests.rs`
  - Dexco: `TestSessionRecordsSubagentNotificationForNextTurn`, `TestSessionSubagentNotificationStatusIsBounded`, `TestSessionSubagentNotificationDuringActiveTurnQueuesFollowUp`
  - Invariant: subagent lifecycle updates can be recorded as user-role contextual fragments wrapped in Codex's `<subagent_notification>` markers, preserve the `agent_path` and JSON-serializable status payload for ordinary statuses, bound oversized status payloads before they become parent-visible context, stay out of ordinary user chat history, are visible to the next model request, and can be recorded during an active parent turn without replacing it by queueing a safe follow-up request.

- `core/tests/suite/tools.rs`
  - Dexco: `TestRouterDispatchReturnsFailedResultForUnknownTool`, `TestRunnerContinuesAfterToolHandlerErrorResult`
  - Invariant: unknown tools and handler errors become failed model-visible outputs instead of hard loop errors.

- `core/src/tools/router_tests.rs` and `core/src/tools/registry_tests.rs`
  - Dexco: `TestRouterDispatchMatchesNamespacedNamesExactly`, `TestRouterSupportsParallelRequiresExactRegisteredOptIn`
  - Invariant: tool names are exact identities and parallel support requires the exact registered handler to opt in.

- `core/src/tools/registry_tests.rs`
  - Dexco: `TestRunnerOptionsHooksCanModifyPromptAndToolResult`
  - Invariant: hooks can rewrite prompt/tool input and post-process model-visible tool output without changing the surrounding loop contract.

- `core/src/tools/registry_tests.rs` and `core/src/tools/tool_dispatch_trace_tests.rs`
  - Dexco: `TestRunnerToolLifecycleHookRecordsCompletedAndFailedOutcomes`
  - Invariant: tool lifecycle observers see per-call start-before-finish records, distinguish handler-completed results with `success=false` from handler-executed failures, and report unsupported tools as failed dispatches where no handler executed while still returning model-visible failed tool outputs.

- `core/tests/suite/hooks.rs`
  - Dexco: `TestBeforeModelRequestHookErrorSkipsModelAndDoesNotPersistInput`, `TestAfterModelRequestHookObservesRetryAttempts`, `TestBeforeToolCallHookErrorSkipsToolDispatch`, `TestAfterToolCallHookErrorStopsBeforeFollowupSampling`
  - Invariant: lifecycle hooks can block before sampling, observe retry attempts, block before side effects, and stop follow-up sampling after post-tool failures.

- `core/tests/suite/safety_check_downgrade.rs` and `core/tests/suite/quota_exceeded.rs`
  - Dexco: `TestRunnerCyberPolicyErrorIsTypedAndNotRetried`, `TestRunnerQuotaExceededIsTypedAndNotRetried`
  - Invariant: provider adapters can normalize cyber-policy/quota failures as typed model errors, and the runner does not retry non-retryable model errors or emit duplicate retry events.

- `core/tests/suite/pending_input.rs`
  - Dexco: `TestSessionSteeredInputTriggersFollowUpRequest`, `TestSessionSteeredInputDoesNotPreemptAfterReasoning`, `TestSessionSteeredInputEnforcesExpectedTurnIDAndReturnsActiveTurnID`, `TestSessionRejectsConcurrentSubmitWhileTurnActive`, `TestSessionSteeredInputInterruptsOptInWaitTool`
  - Invariant: input received while a turn is active is queued separately from durable history, stale expected-turn steering is rejected before it can attach to a newer turn, accepted steering returns the active turn ID, queued input is appended at the next safe model-continuation boundary, steering after a reasoning item does not preempt the active response and preserves reasoning/tool/assistant transcript items before continuation, normal user-turn submission is not allowed to mutate the same session history concurrently, and opt-in wait/sleep-style tools receive pending-input cancellation so they can return an interrupted result before the next model request.

- `core/src/tools/handlers/sleep.rs` and sleep-tool assertions in `core/tests/suite/pending_input.rs`
  - Dexco: `TestSleepHandlerSpecGuardrailAndInterruptOptIn`, `TestSleepHandlerCompletesAndRecordsWallTime`, `TestSleepHandlerInterruptsOnContextCancellation`, `TestSleepHandlerValidatesDuration`
  - Invariant: the optional sleep handler validates `duration_ms` strictly within Codex's 1..12h bounds, rejects unknown fields, is read-only/no-approval, opts into pending-input cancellation, and returns successful model-visible wall-time output for both completed and interrupted sleeps.

- `core/tests/suite/approvals.rs`, Guardian denial circuit-breaker coverage in `core/src/guardian/tests.rs`, and repeated-denial session coverage in `core/src/session/tests.rs`
  - Dexco: `TestGuardrailHookDeniesBeforeReviewerAndSkipsTool`, `TestGuardrailReviewerApprovesRequiredTool`, `TestGuardrailDeniedRequirementSkipsDispatchUnderAllowAll`, `TestGuardrailRepeatedDenialsInterruptTurn`
  - Invariant: permission hooks have precedence, reviewer approval is required when policy asks for it, denied tools do not run, handler-denied calls override broad allow policies, approval request/decision client events are ordered, and repeated same-turn guardrail denials trip a circuit breaker instead of allowing an unbounded denial/follow-up loop.

- `core/tests/suite/request_permissions.rs`, `core/tests/suite/request_permissions_tool.rs`, strict-auto-review coverage in `core/src/session/tests/guardian_tests.rs`, and request-permissions session unit tests
  - Dexco: `TestRequestPermissionsTurnGrantPreapprovesLaterToolInSameTurn`, `TestRequestPermissionsStrictTurnGrantStillRequiresReviewer`, `TestRequestPermissionsPartialGrantDoesNotPreapproveOtherKeys`, `TestRequestPermissionsTurnGrantDoesNotCarryAcrossTurns`, `TestRequestPermissionsSessionGrantCarriesAcrossTurns`, `TestRequestPermissionsHandlerRecordsOnlyGrantedRequestedSubset`, `TestRequestPermissionsHandlerRejectsStrictSessionGrant`, `TestRequestPermissionsHandlerRecordsStrictTurnGrantByKey`
  - Invariant: request-permission responses are intersected with requested grants before storage, granted keys can preapprove later matching guardrails in the same turn, strict turn grants are key-scoped and still route the matching later tool through the reviewer before dispatch, partial grants do not authorize other keys, turn grants expire after the turn, session grants persist across turns, and strict auto-review cannot be converted into a session grant.

- `core/tests/suite/permissions_messages.rs`
  - Dexco: `TestSessionPermissionInstructionsSentOnceAndRefreshedOnChange`, `TestSessionPermissionInstructionsCanBeDisabled`, `TestSessionPermissionInstructionsAreBounded`
  - Invariant: permission instructions are bounded developer prompt fragments rather than durable history, are sent once on session start, are not duplicated while unchanged, append a new fragment when changed, and can be omitted when disabled.

- `core/tests/suite/collaboration_instructions.rs`
  - Dexco: `TestSessionCollaborationInstructionsSentOnceAndRefreshedOnChange`, `TestSessionCollaborationInstructionsCanBeDisabledOrUnset`, `TestSessionEmptyCollaborationInstructionsAreIgnored`, `TestSessionCollaborationInstructionsAreBounded`
  - Invariant: collaboration instructions are bounded contextual developer fragments wrapped in Codex's `<collaboration_mode>` markers, are omitted by default or when disabled, ignore empty updates, replay across turns, append once on meaningful change, avoid no-op duplication, and never become durable conversation history.

- `core/tests/suite/model_visible_layout.rs`
  - Dexco: `TestSessionModelVisibleLayoutOrdersHistoryContextAndDeveloperUpdates`
  - Invariant: previous model-visible history is sent before new contextual turn updates, contextual updates are inserted before the new user input, contextual developer fragments remain outside durable conversation history, and mixed developer updates preserve the order they became model-visible instead of being regrouped by fragment type.

- `core/tests/suite/personality.rs`, excluding config/rollout migration coverage in `core/src/personality_migration_tests.rs` and `core/tests/suite/personality_migration.rs`
  - Dexco: `TestSessionConfiguredInstructionsCarryInitialStyleWithoutPersonalitySpec`, `TestSessionStyleInstructionsAppendPersonalitySpecOnChange`, `TestSessionStyleInstructionsCanBeDisabledOrUnset`, `TestSessionEmptyStyleInstructionsAreIgnored`, `TestSessionStyleInstructionsAreBounded`
  - Invariant: initial/default style is carried as base prompt instructions rather than a `<personality_spec>` update, runtime style changes become bounded contextual developer fragments with Codex's `<personality_spec>` markers and preamble, unchanged values do not append duplicates, empty values emit no new update, disabled/unset style emits nothing, and style updates never become durable conversation history.

- `core/tests/suite/model_switching.rs` and model-switch context tests
  - Dexco: `TestSessionModelSwitchInstructionsAppendOnChange`, `TestSessionModelSwitchSuppressesSameTurnStyleUpdate`, `TestSessionModelSwitchInstructionsAreBounded`
  - Invariant: model changes append bounded contextual developer guidance wrapped in Codex's `<model_switch>` markers, unchanged model-switch guidance does not duplicate prompt context, the fragment never becomes durable conversation history, and same-turn style/personality changes are suppressed because model-switch guidance is the source of truth for the new model's instructions.

- `core/tests/suite/search_tool.rs` and deferred tool registry/search tests
  - Dexco: `TestSessionDeferredToolSearchFindsHiddenToolWithoutAdvertisingIt`, `TestRouterDeferredToolsAreSearchableButNotAdvertised`, `TestRouterToolSearchMatchesNameDescriptionAndSchemaTerms`, `TestRouterToolSearchValidatesQueryAndLimit`
  - Invariant: deferred tools are registered for dispatch but omitted from initial prompt tool specs, `tool_search` is advertised only when deferred tools exist, search indexes deferred tool names, namespace/name separators, descriptions, schema JSON, and extra metadata, returns bounded loadable descriptors, empty query and zero/negative limits fail safely, discovered tools remain history-only and are not injected into later `Prompt.Tools`, and follow-up calls route by exact hidden tool name through the registry.

- `core/tests/suite/user_shell_cmd.rs` and `core/src/user_shell_command_tests.rs`
  - Dexco: `TestSessionRecordsUserShellCommandHistoryForNextTurn`, `TestSessionUserShellCommandOutputIsBoundedAndNotDoubleTruncated`, `TestSessionUserShellCommandRecordDuringActiveTurnQueuesFollowUp`
  - Invariant: completed user-initiated shell commands are persisted as user-role contextual fragments wrapped in Codex's `<user_shell_command>` markers, include command, exit code, duration, and output, are visible to the next model request without becoming ordinary user chat text, can be recorded during an active assistant turn without replacing it, and bound large output without applying the same truncation twice.

- `core/tests/suite/agents_md.rs`, `core/src/context/user_instructions.rs`, and `core/src/context/world_state/agents_md_tests.rs`
  - Dexco: `TestSessionContextInstructionsSnapshotPersistsAndReportsSources`, `TestSessionContextInstructionsAppendReplacementAndRemoval`, `TestSessionContextInstructionsAreBounded`
  - Invariant: AGENTS-style instructions are user-role contextual fragments rendered with Codex's `# AGENTS.md instructions` and `<INSTRUCTIONS>` wrapper, source attribution is exposed to callers but excluded from model-visible diffing, ordinary turns do not rewrite unchanged instruction history, changed instructions append a replacement notice, cleared instructions append a removal notice, and large instruction bodies are bounded before model input.

- `app-server/tests/suite/v2/current_time.rs`
  - Dexco: `TestCurrentTimeHandlerReturnsRFC3339UTCAndReadOnlyGuardrail`
  - Invariant: current-time behavior is read-only and returns a stable UTC time shape.

- `core/tests/suite/shell_command.rs`, `core/tests/suite/shell_serialization.rs`, `core/src/tools/handlers/shell_spec_tests.rs`, portable cases from `core/src/tools/handlers/shell_tests.rs`, portable cases from `core/src/tools/handlers/unified_exec_tests.rs`, shell approval tests, `core/src/command_canonicalization_tests.rs`, `core/src/unified_exec/head_tail_buffer_tests.rs`, `protocol/src/exec_output_tests.rs`, and output-truncation coverage
  - Dexco: `TestExecCommandHandlerGuardrailRequiresApproval`, `TestExecCommandHandlerRunsThroughShellSyntaxAndUnicode`, `TestExecCommandHandlerRunsCommandInWorkdir`, `TestExecCommandHandlerPreservesJSONOutputAsPlainText`, `TestExecCommandHandlerRecordsWallTime`, `TestExecCommandHandlerTruncatesOutput`, `TestTruncateStringKeepsPrefixAndSuffixWhenOverBudget`, `TestTruncateStringSingleCharacterBudgetKeepsTail`, `TestTruncateStringHandlesUnicodeAtRuneBoundary`, `TestDecodeShellOutputUsesSmartEncodingFallbacks`, `TestExecCommandHandlerRejectsEmptyCommand`, `TestExecCommandHandlerTimeoutReturnsFailedResult`, `TestCanonicalizeCommandForApprovalCollapsesPlainShellScripts`, `TestCanonicalizeCommandForApprovalKeepsComplexShellScriptKey`, `TestCanonicalizeCommandForApprovalNormalizesPowerShellWrappers`, `TestCanonicalizeCommandForApprovalPreservesNonShellCommands`, `TestExecCommandHandlerGuardrailCanonicalizesPermissionGrantKey`, `TestRequestPermissionsGrantPreapprovesCanonicalExecCommand`
  - Invariant: shell execution is classified as approval-required before dispatch, runs through a shell with UTF-8 and common Windows legacy output preserved, runs in the requested working directory, returns a model-visible exit-code/wall-time/output envelope, preserves JSON-looking command output as freeform text with exact captured trailing newlines rather than parsing it as JSON, records a positive wall-time duration for real command execution, bounds model-visible output with explicit truncation markers, preserves both prefix and suffix for over-budget output, keeps useful tail text even for very small budgets, truncates Unicode at rune boundaries, rejects empty commands before execution, resolves timeouts as failed tool results, canonicalizes shell wrapper commands before approval/grant matching, preserves exact complex script text under Codex-style script sentinels, and uses the canonical grant key so request_permissions approvals survive incidental shell-spacing differences.

## Partially Portable

- `core/src/tools/context_tests.rs`
  - Dexco now ports content-part text fallback, rich-content preservation, wall-time envelopes, telemetry-preview caps, and tool-output truncation at the provider-neutral `ToolResult` boundary.
  - Remaining gap: Rust Codex's exact `ResponseInputItem` serialization for MCP `CallToolResult`, custom-tool outputs, code-mode raw results, and hosted `ToolSearchOutput` is tied to its Responses wire enum. Port these if Dexco adds provider-specific adapters or a typed MCP call-result wrapper.

- `core/src/tools/handlers/apply_patch_spec_tests.rs`, `core/src/tools/handlers/apply_patch_tests.rs`, `core/src/apply_patch_tests.rs`, `core/src/tools/runtimes/apply_patch_tests.rs`, `core/tests/suite/apply_patch_cli.rs`, and apply-patch cases in `core/tests/suite/shell_serialization.rs`
  - Remaining gap: Dexco currently has function-style `ToolSpec` and handler dispatch only; Rust Codex's `apply_patch` is a freeform/custom tool with hook payloads, streamed diff consumption, and filesystem mutation semantics.
  - Future work: start by adding a freeform tool-spec/payload shape and parser-only result contract before adopting full patch application and sandbox parity.

- `core/src/tools/spec_plan_tests.rs::sleep_tool_follows_current_time_config`
  - Dexco now exposes `SleepHandler` as an optional builtin, but does not add it to `CodingWorkflowHandlers`.
  - Remaining gap: Rust Codex gates `clock.sleep` through `CurrentTimeReminderConfig.sleep_tool` and names it under the `clock` namespace. Dexco's coding workflow router is intentionally stable and provider-neutral; add an explicit options/config surface before making sleep advertised by default.

- `core/tests/suite/token_budget.rs` and `core/src/tools/handlers/{get_context_remaining,new_context_window}*.rs`
  - Dexco ports rollout-budget reminders/exhaustion and context-window marker classification, but not Codex's active context-window accounting.
  - Remaining gap: `get_context_remaining`, `new_context`, context-window IDs, body-after-prefix accounting, manual/automatic context-window resets, compaction item lifecycle, compact hooks, and MCP thread-hint injection depend on Codex's provider token usage, auto-compaction window state, rollout storage, and compaction runtime. Add these only if Dexco grows explicit context-window state and compaction semantics.

- `core/src/event_mapping_tests.rs`
  - Dexco currently ports strict event rejection, reasoning/message history behavior, web-search turn items, hook prompt fragments, contextual user-fragment hiding, multimodal user part normalization, assistant text backward compatibility, and contextual developer-fragment classification.
  - Remaining gap: Dexco does not expose Rust Codex's exact raw `ResponseItem` enum, so parity is asserted at the provider-neutral `ContentPart` boundary.

- `core/src/stream_events_utils_tests.rs`
  - Dexco now ports the provider-neutral assistant memory-citation stripping/parsing behavior through `AssistantTextParser` and `MemoryCitation`.
  - Remaining gap: Codex's extension `TurnItemContributor` hooks, plan-mode `<proposed_plan>` item splitting, mailbox deferral rules, generated-image artifact saving, and external-context pollution checks for hosted provider item variants are tied to Rust Codex's extension registry, plan UI items, inter-agent mailbox, artifact storage, and Responses item enum. Port these only if Dexco adds those surfaces.

- `core/tests/suite/view_image.rs`
  - Remaining gap: model-capability/request-serialization variants, selected local/remote environment routing, and sandbox-deny behavior.
  - Future work: port those only if Dexco grows provider capability negotiation, multi-environment routing, or a file-system sandbox policy.

- `core/tests/suite/request_permissions*.rs`
  - Dexco now exposes generic request-permission grant keys and turn/session grant lifetimes, but not Codex's concrete filesystem/network permission-profile schema.
  - Future work: port path canonicalization, environment-keyed grants, network enablement, and inline `additional_permissions` merging only if Dexco grows a concrete sandbox/permission-profile layer.

- `core/src/exec_policy_tests.rs`
  - Dexco now ports the command-canonicalization part that affects approval/grant matching for the builtin exec tool.
  - Remaining gap: Codex's Starlark exec-policy manager, policy layer stack inheritance, host executable declarations, system/user/project rule loading, and compiled network-domain overlays are tied to Rust Codex configuration and sandbox policy. Dexco intentionally keeps those concrete policies in the embedding application and accepts opaque permission grant keys from tool guardrails.

- `core/tests/suite/permissions_messages.rs`
  - Dexco now ports send-once/change-refresh/disabled semantics for permission instruction fragments.
  - Remaining gap: resume/fork replay and exact rendered permission-profile content including writable roots are tied to Codex rollout storage and concrete sandbox permission profiles.

- `core/tests/suite/collaboration_instructions.rs`
  - Dexco now ports provider-neutral collaboration instruction fragments, including Codex's append-on-change and empty-update behavior.
  - Remaining gap: Codex's full `CollaborationMode` object also carries mode kind, model, reasoning settings, per-turn overrides, and resume/fork rollout replay. Dexco currently keys changes by rendered instruction text because those app/session configuration surfaces are outside the library loop API.

- `core/tests/suite/model_visible_layout.rs`
  - Dexco now ports the provider-neutral prompt ordering invariant for durable history, contextual turn updates, current user input, and chronological developer-fragment replay.
  - Remaining gap: Dexco now has an embedder-supplied environment context renderer, but exact Responses request serialization, resume/fork rollout reconstruction, cwd auto-discovery, and permission-profile rendering remain tied to Codex app/session infrastructure unless Dexco grows corresponding APIs.

- `core/tests/suite/personality.rs`
  - Dexco now ports the provider-neutral style/preamble/update behavior through `StyleInstructionsConfig` and `SetStyleInstructions`.
  - Remaining gap: Codex's product-specific `Personality` enum, feature flag, local/remote model message templates, default pragmatic migration, model-specific base-instruction templating, and app-server thread-setting override behavior remain outside Dexco's provider-neutral library loop. Dexco callers should pass initial style in `Config.Instructions` and use `SetStyleInstructions` only for runtime style updates.

- `core/tests/suite/additional_context.rs`
  - Remaining gap: Dexco does not yet model Codex's UI event hiding for contextual fragments or richer contextual fragment source types beyond the portable additional-context map.
  - Future work: port event-mapping/contextual-fragment visibility tests if Dexco grows UI item streams or multiple context-fragment sources.

- `core/tests/suite/current_time_reminder.rs`
  - Remaining gap: compaction-specific reminder refresh and Codex delivery modes tied to model-only continuations.
  - Future work: port those if Dexco gains compaction/context-window generations or a richer continuation scheduler.

- `core/tests/suite/hooks*.rs` and `core/tests/suite/hooks_mcp.rs`
  - Remaining gap: Codex's external process/config hook runtime, matcher schema, hook JSON payload schema, spilled hook output, session-start hooks, and stop hooks.
  - Future work: port those compatibility tests if Dexco exposes a process/config hook runtime.

- `core/tests/suite/unified_exec*.rs`
  - Dexco has a simple buffered `exec_command`, not Codex's process/session/stdin runtime.
  - Future work: port process lifecycle tests only if Dexco gains long-running process sessions.

- `core/src/exec_env_tests.rs`
  - Remaining gap: Dexco's built-in `exec_command` uses Go's process environment inheritance and does not expose Codex's `ShellEnvironmentPolicy`, thread-ID env injection, permission-profile env injection, Windows PATHEXT repair, include/exclude patterns, or config-driven env overrides.
  - Future work: port this if Dexco grows a first-class execution environment policy; until then embedders that need filtered environments should wrap or replace the builtin exec handler.

- `core/src/shell_tests.rs`, `core/src/shell_snapshot_tests.rs`, `core/src/tasks/user_shell_tests.rs`, and `core/tests/suite/shell_snapshot.rs`
  - Remaining gap: Dexco's builtin exec handler always runs `bash -lc`; it does not discover the user's shell, derive login/non-login argv for bash/zsh/fish/PowerShell/cmd, maintain zsh-fork shell snapshots, prepend runtime paths, or snapshot user-shell startup state.
  - Future work: port these only with an execution-runtime abstraction. Keep the current library handler small and let embedders register a richer shell tool when they own shell discovery/runtime state.

- `core/src/exec_tests.rs`, `core/src/exec_policy_windows_tests.rs`, `core/src/sandbox_tags_tests.rs`, `core/tests/suite/exec.rs`, `core/tests/suite/exec_policy.rs`, `core/tests/suite/unified_exec.rs`, and `core/tests/suite/unified_exec_process_events.rs`
  - Dexco now ports the provider-neutral buffered exec output envelope, approval key canonicalization, timeout-as-failed-result behavior, and output truncation semantics through the builtin `exec_command` handler.
  - Remaining gap: Codex's sandbox-denied string classification, stdout/stderr pipe-drain race handling, long-running unified exec process sessions, stdin writes, process event streams, Windows PowerShell policy parsing, telemetry sandbox tags, and exec policy overlays are concrete runtime/provider concerns outside Dexco's current library handler.
  - Future work: port these only if Dexco grows a first-class process runtime or sandbox policy abstraction; otherwise keep those behaviors in the embedding application or custom tool handler.

- `core/src/tools/runtimes/mod_tests.rs`, `core/src/tools/runtimes/shell_tests.rs`, `core/src/tools/runtimes/shell/unix_escalation_tests.rs`, `core/src/unified_exec/async_watcher_tests.rs`, `core/src/unified_exec/mod_tests.rs`, `core/src/unified_exec/process_manager_tests.rs`, `core/src/unified_exec/process_tests.rs`, and `core/tests/suite/unified_exec_zsh_fork_approvals.rs`
  - Remaining gap: Dexco does not own Codex's runtime registry, process manager, zsh-fork runtime, Unix escalation path, async output watcher, process session IDs, stdin channel, or process lifecycle event protocol.
  - Future work: port these only as part of a first-class long-running process/session runtime; the current `exec_command` handler intentionally remains a small one-shot tool.

- `core/tests/suite/pending_input.rs`
  - Remaining gap: Dexco ports steered-input interruption for opt-in wait/sleep-style tools, but it does not model Codex inter-agent mailbox delivery points or mailbox-triggered wait interruption.
  - Future work: add mailbox pending-input activity if Dexco adopts inter-agent mail.

- `core/tests/suite/user_shell_cmd.rs`
  - Dexco now ports the provider-neutral transcript and active-turn continuation behavior through `RecordUserShellCommand`.
  - Remaining gap: Dexco intentionally does not run user shell commands itself, emit Codex `ExecCommandBegin/Delta/End` UI events, select local/remote environments, manage sandbox env vars, or enforce Codex's one-hour timeout. Embedders execute commands and pass completed records into Dexco.

- `core/tests/suite/agents_md.rs` and `core/src/agents_md_tests.rs`
  - Dexco now ports the provider-neutral instruction snapshot/diff/source-attribution behavior through `ContextInstructionsConfig`, `SetContextInstructions`, `ClearContextInstructions`, and `ContextInstructionSources`.
  - Remaining gap: Dexco intentionally does not discover AGENTS.md files, traverse roots, implement override/default/fallback filename selection, inspect remote filesystems, materialize rollout world-state, reconstruct cold resume/fork state, or model multi-environment source labels. Embedders load bounded snapshots and pass them to Dexco.

- `core/tests/suite/model_switching.rs` and `core/tests/suite/model_overrides.rs`
  - Dexco now ports the provider-neutral `<model_switch>` prompt-update behavior through `ModelSwitchInstructionsConfig` and `SetModelSwitchInstructions`.
  - Remaining gap: Dexco intentionally does not own HTTP model selection, service-tier serialization, model catalog refresh, image-modality filtering after model changes, generated-image artifacts, or provider-specific request routing.

- `core/tests/suite/user_notification.rs`
  - Remaining gap: Dexco emits structured turn-completed client events but does not run Codex's configured external notify scripts or build the CLI notify JSON payload.
  - Future work: embedders can subscribe to `ClientEventTurnCompleted`; add a notification adapter only if Dexco grows a CLI/app runtime that owns process spawning and notification configuration.

- `core/tests/suite/search_tool.rs`
  - Dexco now ports the provider-neutral registry/search/history semantics through `RegisterDeferred` and the synthetic `tool_search` router handler.
  - Remaining gap: Dexco intentionally does not model OpenAI Responses `tool_search_call`/`tool_search_output` wire item types, ChatGPT apps/connectors metadata, MCP prefix/name hashing policy, app auth filtering, multi-agent V1 prompt text, or compaction-specific trimming of search outputs.

- `core/tests/suite/guardian_review.rs`, `core/tests/suite/auto_review.rs`, and the remaining Guardian-specific cases in `core/src/session/tests/guardian_tests.rs`
  - Dexco now ports the generic reviewer seam, approval lifecycle client events, strict turn grant behavior, and repeated-denial breaker.
  - Remaining gap: Codex's Guardian reviewer is a dedicated subagent/provider session with prewarm/reuse, review-model override selection from the remote model catalog, exact Guardian policy prompt, notification isolation, inherited exec-policy isolation, and sandbox/additional-permission validation. Dexco keeps those as host-provided reviewer behavior behind `Guardrails.Reviewer`.
  - Future work: port these only if Dexco owns a Guardian-like reviewer client or model-catalog adapter; otherwise embedders should implement the reviewer callback.

- `core/src/tools/handlers/plan.rs`
  - Dexco now ports the provider-neutral update_plan tool contract, structured client event, malformed-payload handling, and default direct-tool advertisement.
  - Remaining gap: Dexco does not yet have Codex's Plan mode rejection, code-mode nested JSON `{}` tool result, or separate plan/proposed-plan UI item stream. Port those only if Dexco grows plan-mode/code-mode APIs or a richer UI item layer.

- `core/src/session/turn_tests.rs`
  - Remaining gap: Codex plan mode lets extension `TurnItemContributor`s rewrite the last plan-mode agent message before it is stored/displayed. Dexco has hookable prompt/tool boundaries and structured client events, but not Codex's extension registry, plan-mode stream state, or `TurnItem` contributor lifecycle.
  - Future work: port this only if Dexco grows an extension registry that can mutate provider-neutral turn items before commit.

- `core/tests/suite/rollout_budget.rs`
  - Dexco now ports single-session weighted accounting, threshold reminders, and budget-exceeded rejection through `RolloutBudgetConfig` and `ErrRolloutBudgetExceeded`.
  - Remaining gap: Dexco intentionally does not share a budget across parent/child thread trees, rearm reminders after compaction, fail compaction jobs, emit Codex UI error events, or model remote/local compaction token usage. Those depend on Codex thread management, multi-agent, and compaction layers.

- `core/src/context/world_state/environment_render_tests.rs` and `core/src/context/world_state/environment_tests.rs`
  - Dexco now ports the provider-neutral context wrapper and render shape through `EnvironmentContextConfig`, `EnvironmentContextSnapshot`, `SetEnvironmentContext`, and `ClearEnvironmentContext`.
  - Remaining gap: Dexco intentionally does not discover cwd/shell/current date/timezone, render Codex filesystem permission-profile XML, render network sandbox policy, inspect remote environments, track unavailable environment diffs, or reconstruct world-state snapshots across resume/fork.

- `core/src/context/world_state/world_state_tests.rs`
  - Remaining gap: Dexco implements concrete append-only context snapshots directly instead of Codex's generic `WorldStateSection` registry, JSON snapshot merge-patch format, extension-owned world-state sections, retained-fragment matchers, and typed previous-section restoration.
  - Future work: port a generic world-state registry only if Dexco needs third-party extensions to contribute retained/diffed prompt fragments independently of the session APIs already exposed.

- `core/tests/suite/subagent_notifications.rs`
  - Dexco now ports the provider-neutral parent-visible notification fragment through `RecordSubagentNotification`.
  - Remaining gap: Dexco intentionally does not spawn child threads, inherit parent developer context/history, implement wait-agent mailbox blocking, run subagent lifecycle hooks, encode `agent_message` Responses wire items, or share rollout budgets across subagent trees. Those require a multi-agent runtime.

- `core/src/turn_metadata_tests.rs`
  - Dexco now ports the provider-neutral same-turn state lifetime through `Prompt.TurnState` and `ResponseEvent.TurnState`.
  - Remaining gap: Dexco does not expose request metadata maps, Responses/WebSocket header serialization, installation/window IDs, sandbox tags, workspace enrichment, or fork/subagent lineage metadata.
  - Future work: if Dexco grows provider request adapters, add protected `Prompt`/request metadata fields so caller metadata cannot override runner-owned turn IDs, request kinds, timestamps, or provider metadata keys.

- `core/tests/suite/safety_check_downgrade.rs`
  - Remaining gap: Dexco does not have Codex's model-reroute warning surface or model-verification suppression logic.
  - Future work: port model reroute/model-verification events if Dexco grows provider model-header verification or model routing.

- `core/tests/suite/responses_lite.rs`
  - Remaining gap: Dexco does not expose OpenAI request encoders, Responses Lite headers, hosted-tool-to-standalone-extension substitution, provider auth-header checks, or compaction transport contracts.
  - Future work: if Dexco grows provider request adapters, add an optional Responses Lite encoder that serializes instructions/tools as input items, keeps hosted tools out of `tools`, preserves the Responses Lite header/compaction contract, and reuses Dexco's existing bounded remote-image placeholder behavior.

- `core/src/tools/network_approval_tests.rs`, `core/src/network_policy_decision_tests.rs`, `core/src/tools/sandboxing_tests.rs`, `core/src/safety_tests.rs`, and shell-runtime tests under `core/src/tools/runtimes/*`
  - Remaining gap: Dexco ports the provider-neutral approval/reviewer/request-permission seams, but not Codex's concrete network proxy approval service, filesystem sandbox policy evaluator, exec-server environment payloads, managed network sandbox context, zsh-fork snapshot runtime, or apply-patch safety policy.
  - Future work: port these only if Dexco grows first-class network/filesystem sandbox policy and long-running shell runtime adapters; until then callers should supply opaque guardrail requirements and permission grant keys.

- `core/src/client_tests.rs`, `core/tests/responses_headers.rs`, `core/tests/suite/client_websockets.rs`, `core/tests/suite/websocket_fallback.rs`, `core/tests/suite/request_compression.rs`, `core/tests/suite/window_headers.rs`, `core/src/util_tests.rs`, `core/src/tasks/mod_tests.rs`, and `core/src/state/session_tests.rs`
  - Remaining gap: Dexco now ports the portable prompt/history/raw-event/turn-state pieces, but not Codex's HTTP/WebSocket request encoders, connection prewarm/reuse/fallback, compression headers, W3C trace propagation, auth recovery telemetry, attestation, window IDs across compact/resume/fork, OpenTelemetry task metrics, connector selections, or typed global rate-limit state.
  - Future work: add these only with a provider/client adapter layer or state-store layer; keep the current core library focused on the model loop contract.

- `protocol/src/error_tests.rs`
  - Remaining gap: Dexco ports non-retryable typed model errors at the loop boundary, but not Codex's provider-specific usage-limit copy, HTTP unexpected-status formatting, sandbox UI error aggregation, or protocol error-event mapping.
  - Future work: add these only with a concrete provider/client adapter or UI protocol layer; keeping them out of the core library avoids baking OpenAI account-plan copy into Dexco's model loop.

- Subagent-only coverage in `core/src/tools/handlers/request_user_input_tests.rs`
  - Remaining gap: Dexco does not own Codex parent/child thread routing, so it cannot enforce "request_user_input only from root thread" internally.
  - Future work: if Dexco grows a first-class subagent runtime, gate user-interaction tools at the parent/root boundary before dispatch.

- `core/src/mcp_tool_call/telemetry_tests.rs`, `core/src/mcp_tool_exposure_test.rs`, `core/src/session/mcp_tests.rs`, `core/tests/suite/hooks_mcp.rs`, `core/tests/suite/mcp_refresh_cleanup.rs`, `core/tests/suite/mcp_tool_exposure.rs`, `core/tests/suite/mcp_turn_metadata.rs`, `core/tests/suite/openai_file_mcp.rs`, and `core/tests/suite/rmcp_client.rs`
  - Remaining gap: Dexco has provider-neutral tool-result previews and guardrail approvals, but not Codex's typed MCP invocation wrapper, app connector identity, auth-failure metadata, elicitation review routing, or OpenTelemetry tag normalization.
  - Future work: port these when Dexco adds a first-class MCP adapter; keep the current core API at generic `ToolCall`, `ToolResult`, `ToolGuardrail`, and client-event boundaries.

- `core/src/tools/handlers/mcp_resource_spec_tests.rs`, `core/src/tools/handlers/mcp_resource_tests.rs`, and `core/src/tools/handlers/mcp_search_tests.rs`
  - Remaining gap: Dexco has generic tools and deferred tool search, but not Codex's MCP resource list/read tools, per-server cursor handling, bounded resource-read output, MCP-specific source metadata, or MCP namespace/hash policy.
  - Future work: port these only with a first-class MCP adapter; the likely Dexco shape is a small `internal/mcp` package plus router tests for MCP-backed resources and search metadata.

- `core/src/tools/handlers/multi_agents_spec_tests.rs`, `core/src/tools/handlers/multi_agents_tests.rs`, `core/src/tools/handlers/agent_jobs_spec_tests.rs`, `core/src/tools/handlers/agent_jobs_tests.rs`, `core/src/agent/control_tests.rs`, `core/src/agent/control/execution_tests.rs`, `core/src/agent/control/residency_tests.rs`, `core/src/agent/registry_tests.rs`, `core/src/agent/role_tests.rs`, `core/tests/suite/agent_execution.rs`, `core/tests/suite/agent_jobs.rs`, `core/tests/suite/agent_websocket.rs`, `core/tests/suite/multi_agent_mode.rs`, and `core/tests/suite/spawn_agent_description.rs`
  - Remaining gap: Dexco ports parent-visible subagent notifications only. It does not own Codex's agent manager, spawn/send/wait/list/interrupt/close tools, encrypted mailbox messages, concurrency/depth limits, role registry, agent residency, job CSV parsing, delegation prompt policy, or WebSocket/runtime status mapping.
  - Future work: port these as a separate multi-agent runtime package only if Dexco should manage child sessions itself; until then embedders can compose multiple Dexco sessions around the current library loop.

- `protocol/src/capabilities_tests.rs`
  - Remaining gap: Dexco does not expose Codex capability-root selections, plugin roots, or foreign-environment `file://` URI parsing at the API boundary.
  - Future work: port this if Dexco grows a capability/plugin root selector that needs to preserve executor-native paths across app and exec operating systems.

- `core/src/plugins/render_tests.rs`, `core/src/plugins/mentions_tests.rs`, `core/src/plugins/discoverable_tests.rs`, and `core/tests/suite/plugins.rs`
  - Remaining gap: Dexco hides normalized recommended-plugin context fragments when adapters provide them, but it does not discover plugins, parse `plugin://` or `app://` user mentions, render Codex plugin-use instructions, fetch marketplace catalogs, or filter installed/discoverable plugin state.
  - Future work: port these only if Dexco grows explicit plugin/app capability APIs; otherwise embedders should render plugin guidance as ordinary developer context and expose capabilities through tools/skills they register.

- `core/src/tools/handlers/request_plugin_install_tests.rs` and `core/tests/suite/request_plugin_install.rs`
  - Remaining gap: Dexco does not own Codex plugin/app install suggestions, legacy argument compatibility, remote/core installed verification, decline-always persistence, config.toml mutation, discoverable endpoint recommendations, or plugin manager cache refresh.
  - Future work: port these only if Dexco grows a plugin/app capability API; until then embedders should expose install flows through their own tools and approval/reviewer callbacks.

- `core/src/codex_delegate_tests.rs` and `core/tests/suite/codex_delegate.rs`
  - Remaining gap: Dexco ports generic reasoning deltas and guardrail approval events, but not Codex's delegated review mode, parent/child event forwarding, approval round-trips through a parent session, blocked-channel shutdown behavior, trace propagation through delegated ops, or review-mode lifecycle UI events.
  - Future work: port this with a review/delegate runtime API, likely sharing the same infrastructure as a future multi-agent runtime rather than adding it to the core model loop.

- `core/src/thread_rollout_truncation_tests.rs` and `core/src/turn_diff_tracker_tests.rs`
  - Remaining gap: Dexco does not persist Codex rollout items, fork by canonical turn ID, apply rollback markers, track inter-agent trigger-turn boundaries, or reconstruct Git diffs from apply-patch deltas across environment display roots.
  - Future work: port these only if Dexco adds a durable rollout/fork store or an apply-patch/Git-diff subsystem; the current library loop stores in-memory prompt history and leaves filesystem mutation tracking to embedders.

- `core/src/git_info_tests.rs`
  - Remaining gap: Dexco does not collect Git repository metadata, recent commits, remote diff summaries, trust roots, or rollout `SessionMeta` Git fields.
  - Future work: port this only if Dexco grows repository-inspection context or durable rollout metadata. For now embedders can add Git summaries as bounded additional/context-instruction fragments.

- `core/src/session/code_mode_warning_tests.rs`
  - Remaining gap: Dexco does not own Codex feature flags, code-mode tool-mode selectors, fallback model metadata, or warnings for unsupported model/tool-mode combinations.
  - Future work: port this with a provider model-catalog layer; the current library accepts normalized tools and prompt metadata from callers.

- `core/src/session/rollout_reconstruction_tests.rs`
  - Remaining gap: Dexco does not reconstruct sessions from Codex rollout JSONL, hydrate previous turn settings, restore world-state baselines, or reinterpret inter-agent rollout items during resume/fork.
  - Future work: port this only if Dexco adopts a durable rollout store and resume/fork APIs.

- `core/src/config/auth_keyring_tests.rs`, `core/src/config/config_loader_tests.rs`, `core/src/config/config_tests.rs`, `core/src/config/edit_tests.rs`, `core/src/config/network_proxy_spec_tests.rs`, `core/src/config/permissions_tests.rs`, `core/src/config/schema_tests.rs`, `core/src/network_proxy_loader_tests.rs`, and `core/tests/suite/unstable_features_warning.rs`
  - Remaining gap: Dexco exposes explicit library configuration structs and does not load `config.toml`, mutate layered config, manage keyrings, render schema fixtures, load network proxy config, or emit Codex app warnings for unstable feature flags.
  - Future work: port these only if Dexco grows a Codex-compatible app configuration layer; keep config loading in embedders for now.

- `core/src/compact_tests.rs`, `core/tests/suite/compact.rs`, `core/tests/suite/compact_remote.rs`, `core/tests/suite/compact_remote_parity.rs`, `core/tests/suite/compact_resume_fork.rs`, and `core/tests/suite/image_rollout.rs`
  - Remaining gap: Dexco ports bounded context fragments and retry-safe history behavior, but not Codex's local/remote compaction jobs, rollout-image serialization, compaction prompt assembly, resume/fork compact replay, or remote parity transport.
  - Future work: port these only with a durable rollout store and explicit compaction API.

- `core/src/thread_manager_tests.rs`, `core/tests/common/test_environment_tests.rs`, `core/tests/suite/fork_thread.rs`, `core/tests/suite/resume.rs`, `core/tests/suite/resume_warning.rs`, `core/tests/suite/rollout_list_find.rs`, and `core/tests/suite/sqlite_state.rs`
  - Remaining gap: Dexco sessions are in-memory library objects. Codex's thread manager, test environment harness, fork tree, resume warnings, rollout list/find, and SQLite state tests require app-owned persistence and thread orchestration.
  - Future work: port these only if Dexco adds durable session/thread storage.

- `core/tests/all.rs`, `core/tests/common/apps_test_server.rs`, `core/tests/common/context_snapshot.rs`, `core/tests/common/hooks.rs`, `core/tests/common/lib.rs`, `core/tests/common/process.rs`, `core/tests/common/responses.rs`, `core/tests/common/streaming_sse.rs`, `core/tests/common/test_codex.rs`, `core/tests/common/test_codex_exec.rs`, `core/tests/common/test_environment.rs`, `core/tests/common/tracing.rs`, and `core/tests/common/zsh_fork.rs`
  - Remaining gap: these are Rust Codex integration-test harness files and helpers for mock Responses servers, app-server/MCP fixtures, process helpers, SSE streaming, tracing capture, zsh-fork setup, and `TestCodex` construction. They are not product behavior for Dexco to port directly.
  - Future work: add Dexco-local test helpers only when a Go parity test needs equivalent fixtures.

- `core/tests/remote_env_windows/remote_env_windows_test.rs`
  - Remaining gap: this is Bazel-only Wine coverage for a Windows exec-server, native PowerShell shell selection, foreign-path cwd handling, and remote apply-patch/exec-server integration. Dexco has no remote exec-server runtime or Windows path URI translator.
  - Future work: port this only with a cross-OS remote-execution adapter.

- `core/src/realtime_context_tests.rs`, `core/src/realtime_conversation_tests.rs`, and `core/tests/suite/realtime_conversation.rs`
  - Remaining gap: Dexco models a request/stream loop, not Codex's realtime conversation context, audio/realtime session state, or realtime transport.
  - Future work: port these with a dedicated realtime adapter, not the current synchronous model-client interface.

- `core/src/connectors_tests.rs`, `core/tests/suite/skills.rs`, and `core/tests/suite/skill_approval.rs`
  - Remaining gap: Dexco does not discover connectors or skills, resolve capability roots, run skill approval flows, or inject Codex marketplace/app connector metadata.
  - Future work: port these only if Dexco owns capability discovery; otherwise embedders can render skills/connectors as tools or contextual instructions.

- `core/tests/suite/model_runtime_selectors.rs`, `core/tests/suite/models_cache_ttl.rs`, `core/tests/suite/remote_models.rs`, `core/tests/suite/override_updates.rs`, and `core/tests/suite/remote_env.rs`
  - Remaining gap: Dexco does not own Codex model catalogs, runtime selector validation, model cache TTLs, remote model headers, per-turn override update APIs, or remote environment selection.
  - Future work: port these with a provider/catalog adapter and environment registry.

- `core/tests/suite/cli_stream.rs`, `core/tests/suite/live_cli.rs`, `core/tests/suite/prompt_debug_tests.rs`, `core/tests/suite/deprecation_notice.rs`, `core/tests/suite/responses_api_proxy_headers.rs`, and `core/tests/suite/review.rs`
  - Remaining gap: Dexco is a library and does not implement Codex CLI streaming, live CLI integration, prompt-debug endpoints, deprecation notices, Responses API proxy headers, or review-mode CLI behavior.
  - Future work: keep these in application adapters unless Dexco grows a CLI/server package.

- `core/tests/suite/code_mode.rs`
  - Remaining gap: Dexco does not implement Codex code-mode custom tool transport, Apps/MCP-backed dynamic tool loading, code-cell lifecycle, code-mode image/file rollout behavior, or code-mode-specific tool-result serialization.
  - Future work: port this only if Dexco grows a code-mode runtime distinct from the generic tool loop.

- `core/tests/suite/otel.rs`
  - Remaining gap: Dexco exposes library events and metrics but does not emit Codex OpenTelemetry spans/events, service-tier/model attributes, MCP telemetry fields, or session source/auth-mode attributes.
  - Future work: add an optional telemetry adapter if Dexco needs Codex-compatible tracing; keep the core loop telemetry-neutral.

- `core/src/tools/handlers/test_sync_spec_tests.rs` and `core/tests/suite/mod.rs`
  - Remaining gap: Codex's `test_sync_tool` and suite module aggregator are integration-test harness infrastructure, not product/library behavior. Dexco does not need this helper unless its own integration tests require a model-visible synchronization tool.
  - Future work: add a Dexco-local test helper only if parallel-tool integration tests need barrier synchronization beyond current unit tests.

- `core/src/windows_sandbox_tests.rs`, `core/src/windows_sandbox_read_grants_tests.rs`, `core/tests/suite/windows_sandbox.rs`, `core/tests/suite/network_approval.rs`, and `core/tests/suite/extension_sandbox.rs`
  - Remaining gap: Dexco has no OS sandbox backend, Windows read-grant translator, extension sandbox process isolation, network proxy approval daemon, or filesystem policy engine.
  - Future work: port these only with a host-owned sandbox abstraction; otherwise keep sandbox enforcement outside the Dexco library loop.

## Non-Core Codex Test Clusters

- App-server API and transport tests under `app-server*/**/*tests.rs`, `app-server/tests/**/*.rs`, and `app-server-protocol/tests/schema_fixtures.rs`
  - Dexco-relevant behavior is already covered directly at the Go library boundary for output schemas, web-search prompt metadata/items, current-time reminders, sleep, request-user-input, request-permissions, plan updates, turn steering, image detail, style/collaboration/model overrides, and turn metrics.
  - Remaining gap: these Rust tests validate JSON-RPC v2 request/response/notification shapes, schema fixtures, websocket/unix-socket transport, remote control, app-server process lifecycle, thread storage, environment selection, config RPCs, auth/account APIs, marketplace/plugin APIs, MCP endpoints, rate-limit endpoints, and connected app-server/exec-server behavior. Dexco intentionally has no app-server protocol layer.
  - Examples reviewed: `app-server/tests/suite/v2/output_schema.rs`, `app-server/tests/suite/v2/web_search.rs`, `app-server/tests/suite/v2/sleep.rs`, `app-server/tests/suite/v2/request_user_input.rs`, `app-server/tests/suite/v2/request_permissions.rs`, `app-server/tests/suite/v2/plan_item.rs`, `app-server/tests/suite/v2/turn_steer.rs`, `app-server/tests/suite/v2/turn_interrupt.rs`, `app-server/tests/suite/v2/turn_start.rs`, and `app-server/tests/suite/v2/current_time.rs`.
  - Future work: only add app-server compatibility tests if Dexco grows its own JSON-RPC/server adapter. Keep current parity tests on Dexco's `Session`, `Runner`, `ToolResult`, and client-event APIs.

- CLI and executable frontend tests under `cli/tests/*.rs`, `exec/src/*_tests.rs`, `exec/tests/**/*.rs`, `chatgpt/tests/**/*.rs`, and `apply-patch/tests/**/*.rs`
  - Remaining gap: these tests cover command-line parsing, login/update/debug commands, app-server startup from CLI, exec-server process startup, human/JSON/JSONL output renderers, stdin prompt handling, CLI approval policy flags, ephemeral/resume behavior, external hook execution, and standalone apply-patch CLI scenarios.
  - Future work: port only reusable library contracts that appear behind those frontends. Current reusable contracts are already covered in Dexco by exec output handling, output schema propagation, AGENTS/context instructions, hook prompt fragments, guardrail approvals, and retry/error behavior.

- Execution, sandbox, and remote environment crates under `exec-server/**/*.rs`, `execpolicy*/**/*.rs`, `linux-sandbox/**/*.rs`, `windows-sandbox-rs/**/*.rs`, `sandboxing/**/*.rs`, and `network-proxy/**/*.rs`
  - Remaining gap: Dexco's builtin `exec_command` is a one-shot library tool. These Rust tests cover process sessions, file streaming, selected capability roots, Noise/WebSocket/HTTP transports, remote filesystem path URI parsing, sandbox policy transformation, OS-specific sandboxing, network proxy rules, and legacy exec-policy parsers.
  - Future work: port only if Dexco grows first-class exec-server/sandbox adapters. Until then embedders should enforce concrete sandbox and remote-exec policies outside Dexco and expose opaque guardrail keys to the library.

- Vendored Bubblewrap tests under `codex-rs/vendor/bubblewrap/tests/**`
  - Remaining gap: these are third-party sandbox dependency tests for Bubblewrap itself. Dexco does not vendor or test Bubblewrap.
  - Future work: none for Dexco unless it adopts a concrete Bubblewrap sandbox backend.

- Configuration, provider, auth, and state support crates under `config/**/*.rs`, `cloud-config/**/*.rs`, `cloud-tasks/**/*.rs`, `backend-client/**/*.rs`, `model-provider*/**/*.rs`, `models-manager/**/*.rs`, `login/**/*.rs`, `state/**/*.rs`, `thread-store/**/*.rs`, `rollout*/**/*.rs`, `message-history/**/*.rs`, and `features/**/*.rs`
  - Remaining gap: Dexco uses explicit Go config structs and in-memory sessions. These Rust tests cover layered TOML config, cloud config bundles, hooks/MCP config parsing, auth/keyring behavior, model catalogs and rate limits, rollout/thread stores, persistent state, feature flags, and backend/cloud task integrations.
  - Future work: add Dexco adapters only if the library takes ownership of Codex-compatible config or durable storage. The current library should continue accepting normalized configuration from its embedder.

- MCP, connector, plugin, and skill support crates under `codex-mcp/**/*.rs`, `mcp-server/**/*.rs`, `rmcp-client/**/*.rs`, `connectors/**/*.rs`, `core-plugins/**/*.rs`, `core-skills/**/*.rs`, `plugin/**/*.rs`, and `codex-home/**/*.rs`
  - Remaining gap: Dexco has generic tools, deferred tool search, contextual skill/plugin fragments, and guardrail seams, but not Codex's MCP connection manager, catalog/cache, connector policy, plugin marketplace/install/store/share/runtime, or skill discovery/injection services.
  - Future work: port these as optional adapters only if Dexco should discover and manage capabilities itself. Otherwise embedders can register tools and inject bounded instructions through existing Dexco APIs.

- Code-mode and realtime/provider transport crates under `code-mode*/**/*.rs`, `codex-api/**/*.rs`, `codex-client/**/*.rs`, and realtime app/core tests
  - Remaining gap: Dexco models a non-realtime request/stream loop. These tests cover code-cell runtimes, stdio/host protocol, remote code-mode sessions, SSE/WebSocket provider clients, realtime websocket APIs, outbound proxy/CA config, and API bridge behavior.
  - Future work: add separate adapters if Dexco should support code-mode or realtime transports. Do not fold those concerns into the core runner/session APIs.

- Observability, utilities, TUI, and miscellaneous crates under `analytics/**/*.rs`, `otel/**/*.rs`, `utils/**/*.rs`, `tools/**/*.rs`, `tui/**/*.rs`, `ext/**/*.rs`, `hooks/**/*.rs`, `memories/**/*.rs`, `prompts/**/*.rs`, `terminal-detection/**/*.rs`, `uds/**/*.rs`, `stdio-to-uds/**/*.rs`, `external-agent-sessions/**/*.rs`, `git-utils/**/*.rs`, `file-watcher/**/*.rs`, and `ollama/**/*.rs`
  - Remaining gap: these tests cover telemetry event emission, analytics clients, TUI rendering/snapshots, extension hosts, hook process runtime, memory/prompt helpers, path/terminal utilities, local IPC helpers, external-agent sessions, git/file watchers, and Ollama integration.
  - Future work: port only provider-neutral contracts that become part of Dexco's public library API. Current event/metrics parity is intentionally exposed as client events and turn metrics rather than Codex's telemetry/logging stack.

- SDK, package, lint, and Bazel test infrastructure under `sdk/typescript/tests/**/*.ts`, `sdk/python/tests/**/*.py`, `scripts/codex_package/test_*.py`, `scripts/test-remote-env.sh`, `tools/argument-comment-lint/test_wrapper_common.py`, and `bazel/rules/testing/**/*.rs`
  - Remaining gap: these tests validate TypeScript/Python SDK APIs over app-server boundaries, CLI child-process argument/env handling, generated contract/runtime behavior, packaging target selection, argument-comment-lint wrapper behavior, remote-env smoke scripts, and Bazel Wine test helpers. They do not exercise Dexco's Go library loop directly.
  - Future work: port only if Dexco grows SDKs, package scripts, lint wrappers, or Bazel/Wine test infrastructure. Current Dexco parity remains at the library API boundary.

## Next High-Value Gaps

- None identified in Dexco's current library surface. Add entries here when a remaining Codex test maps to provider-neutral loop, tool, guardrail, prompt-context, retry, or client-event behavior rather than UI/app-server/storage/sandbox/provider-transport infrastructure.

## Out Of Scope For Current Dexco

- App-server JSON-RPC protocol, schema fixtures, websocket, remote-control, and thread management tests.
- TUI/rendering/snapshot tests.
- Realtime conversation tests.
- Cloud, remote environment, MCP, connector, plugin, skill, and multi-agent runtime tests.
- OS sandbox backend, Windows sandbox, Seatbelt/Landlock, network proxy, zsh-fork, and platform escalation tests.
- Rollout storage, SQLite state, resume/fork/truncation, compaction, token-budget, and prompt-cache implementation tests.
- Model-provider HTTP header, auth, quota, model switching, and hosted response transport tests.
