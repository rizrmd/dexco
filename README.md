# dexco

`dexco` is a Go extraction of the core non-realtime LLM loop from the Rust Codex agent.

It can be imported as a library:

```go
import "github.com/openai/codex/dexco"

router, err := dexco.NewDefaultRouter(responder)
runner, err := dexco.NewRunner(modelClient, router)
session, err := dexco.NewSession(dexco.Config{Instructions: "Be concise."}, runner)

result, err := session.SubmitUserInput(ctx, dexco.OpUserInput{
	Input: dexco.UserInput{Content: "inspect the workspace"},
}, dexco.NopSink{})
```

Optional Codex-style runtime features are configured on the runner:

```go
runner, err := dexco.NewRunnerWithOptions(modelClient, router, dexco.RunnerOptions{
	MaxModelRetries: 2,
	ParallelTools: true,
	Hooks: dexco.Hooks{
		BeforeModelRequest: beforeModelRequest,
		BeforeToolCall:     beforeToolCall,
		AfterToolCall:      afterToolCall,
	},
})
```

Guardrails are opt-in at the runner level. A handler can implement
`dexco.GuardedHandler` to classify a tool call, and the runner can require an
approval reviewer for sensitive calls. Permission hooks run first; if they
return `ApprovalDecisionNoDecision`, Dexco falls back to the reviewer.

```go
runner, err := dexco.NewRunnerWithOptions(modelClient, router, dexco.RunnerOptions{
	Guardrails: dexco.Guardrails{
		ApprovalPolicy: dexco.ApprovalPolicyRequireForSensitive,
		Reviewer: func(ctx context.Context, turn dexco.Turn, req dexco.ToolApprovalRequest) (dexco.ApprovalDecision, error) {
			if req.Guardrail.Risk == dexco.ToolRiskCommandExecution {
				return dexco.ApprovalDecisionDenied, nil
			}
			return dexco.ApprovalDecisionApproved, nil
		},
	},
})
```

The built-in `exec_command` handler marks shell execution as
`ApprovalRequirementRequired`, so it is gated when
`ApprovalPolicyRequireForSensitive` is enabled. If `ParallelTools` is enabled,
approval hooks and reviewers for parallel-safe tools may run concurrently and
should be concurrency-safe.

Sinks can also opt into raw provider stream events by implementing
`dexco.ResponseEventSink`. This is how callers can observe richer Codex-style
events such as item starts, streamed tool-call input deltas, model metadata,
rate limits, and token usage without forcing every sink to implement every
event callback.

For a single typed event stream, implement `dexco.ClientEventSink`. It receives
turn lifecycle events, deltas, tool calls/results, tool approval
requests/decisions, raw response events, and retry notifications.

Current scope:

1. accept user input
2. build a prompt from history, instructions, and tool specs
3. stream model events
4. expose raw response events to interested library consumers
5. optionally run hooks and retry failed model stream starts
6. optionally gate tool calls through guardrail hooks and approval reviewers
7. dispatch completed tool calls, optionally using parallel-safe handlers
8. append tool outputs back into history
9. repeat until no follow-up work remains

The first cut intentionally keeps the surface small so the loop can be tested in isolation before porting more of the session and app-server layers.

Current package layout:

- root package: public library facade for callers
- `internal/model`: turn, prompt, item, tool-call, and stream-event types
- `internal/events`: sink interface for UI or transport updates
- `internal/tools`: tool registry/router
- `internal/tools/builtin`: concrete built-in tool handlers
- `internal/runner`: the core `[1]` through `[13]` loop
- `internal/session`: `Op::UserInput`-style session entrypoint and history persistence
