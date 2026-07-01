package dexco_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openai/codex/dexco"
)

type scriptedClient struct {
	t       *testing.T
	prompts []dexco.Prompt
	calls   int
}

func (c *scriptedClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	c.calls++

	switch c.calls {
	case 1:
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "call-1",
			Name:      "echo_tool",
			Arguments: json.RawMessage(`{"value":"workspace"}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 2:
		if !containsToolResult(prompt.History, "call-1", "workspace") {
			c.t.Fatalf("second prompt missing tool result history: %#v", prompt.History)
		}
		item := dexco.AssistantMessageItem("done")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", c.calls)
		return nil, nil
	}
}

type sliceStream struct {
	events []dexco.ResponseEvent
	index  int
}

func (s *sliceStream) Recv() (dexco.ResponseEvent, error) {
	if s.index >= len(s.events) {
		return dexco.ResponseEvent{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

type echoTool struct{}

func (echoTool) Name() string {
	return "echo_tool"
}

func (echoTool) Spec() dexco.ToolSpec {
	return dexco.ToolSpec{
		Name:        "echo_tool",
		Description: "Returns the supplied value.",
		Parameters: map[string]any{
			"type": "object",
		},
	}
}

func (echoTool) Call(_ context.Context, call dexco.ToolCall) (dexco.ToolResult, error) {
	var args struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return dexco.ToolResult{}, err
	}
	return dexco.ToolResult{
		Output:  args.Value,
		Success: true,
	}, nil
}

func TestLibrarySessionRunsToolLoop(t *testing.T) {
	t.Parallel()

	router, err := dexco.NewRouter(echoTool{})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &scriptedClient{t: t}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{Instructions: "Be concise."}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "run echo"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "done" {
		t.Fatalf("FinalMessage = %q, want %q", result.FinalMessage, "done")
	}
	if result.ModelCalls != 2 {
		t.Fatalf("ModelCalls = %d, want 2", result.ModelCalls)
	}
}

type toolSpecStabilityClient struct {
	t       *testing.T
	prompts []dexco.Prompt
}

func (c *toolSpecStabilityClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	if len(c.prompts) == 1 {
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "call-1",
			Name:      "echo_tool",
			Arguments: json.RawMessage(`{"value":"ok"}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	}
	if !reflect.DeepEqual(c.prompts[1].Tools, c.prompts[0].Tools) {
		c.t.Fatalf("follow-up Prompt.Tools = %#v, want stable first tools %#v", c.prompts[1].Tools, c.prompts[0].Tools)
	}
	item := dexco.AssistantMessageItem("stable tools")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

// Adapted from Codex prompt-caching tests. Dexco does not own provider cache
// keys or HTTP request serialization, but it does own Prompt.Tools construction:
// follow-up sampling requests must see the same stable tool spec list rather
// than drifting after a tool result is appended to history.
func TestSessionPromptToolSpecsStableAcrossFollowUpRequests(t *testing.T) {
	t.Parallel()

	router, err := dexco.NewRouter(echoTool{})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &toolSpecStabilityClient{t: t}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "run echo"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "stable tools" {
		t.Fatalf("FinalMessage = %q, want stable tools", result.FinalMessage)
	}
	if len(client.prompts) != 2 {
		t.Fatalf("prompt count = %d, want 2", len(client.prompts))
	}
	if got := promptToolNames(client.prompts[0].Tools); !reflect.DeepEqual(got, []string{"echo_tool"}) {
		t.Fatalf("first prompt tools = %#v, want echo_tool", got)
	}
}

type turnStateClient struct {
	t       *testing.T
	prompts []dexco.Prompt
	stable  bool
}

func (c *turnStateClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	switch len(c.prompts) {
	case 1:
		if prompt.TurnState != "" {
			c.t.Fatalf("first prompt TurnState = %q, want empty", prompt.TurnState)
		}
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "turn-state-call-1",
			Name:      "echo_tool",
			Arguments: json.RawMessage(`{"value":"first-followup"}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventCreated, TurnState: "ts-1"},
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 2:
		if prompt.TurnState != "ts-1" {
			c.t.Fatalf("follow-up prompt TurnState = %q, want ts-1", prompt.TurnState)
		}
		if c.stable {
			item := dexco.ToolCallItem(dexco.ToolCall{
				CallID:    "turn-state-call-2",
				Name:      "echo_tool",
				Arguments: json.RawMessage(`{"value":"second-followup"}`),
			})
			return &sliceStream{
				events: []dexco.ResponseEvent{
					{Type: dexco.EventCreated, TurnState: "ts-2"},
					{Type: dexco.EventOutputItemDone, Item: &item},
					{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
				},
			}, nil
		}
		item := dexco.AssistantMessageItem("first turn done")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 3:
		if c.stable {
			if prompt.TurnState != "ts-1" {
				c.t.Fatalf("second follow-up prompt TurnState = %q, want ts-1", prompt.TurnState)
			}
			item := dexco.AssistantMessageItem("stable turn state")
			return &sliceStream{
				events: []dexco.ResponseEvent{
					{Type: dexco.EventOutputItemDone, Item: &item},
					{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
				},
			}, nil
		}
		if prompt.TurnState != "" {
			c.t.Fatalf("next turn prompt TurnState = %q, want empty", prompt.TurnState)
		}
		item := dexco.AssistantMessageItem("second turn done")
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

// Adapted from Codex turn_state tests. Rust Codex receives
// `x-codex-turn-state` from the transport and replays it only for follow-up
// requests within the same logical turn. Dexco keeps the same contract as
// provider-neutral Prompt metadata so HTTP/websocket adapters can choose their
// own wire encoding without making the token durable conversation history.
func TestSessionTurnStatePersistsWithinTurnAndResetsAfter(t *testing.T) {
	t.Parallel()

	router, err := dexco.NewRouter(echoTool{})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &turnStateClient{t: t}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "run echo"},
	}, dexco.NopSink{}); err != nil {
		t.Fatalf("SubmitUserInput(first) error = %v", err)
	}
	if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "next turn"},
	}, dexco.NopSink{}); err != nil {
		t.Fatalf("SubmitUserInput(second) error = %v", err)
	}

	if len(client.prompts) != 3 {
		t.Fatalf("prompts = %d, want 3", len(client.prompts))
	}
}

// Adapted from Codex's websocket turn-state stability test. The provider may
// send another state token on a later same-turn response, but Codex stores the
// first value in an OnceLock. Dexco mirrors that first-value-wins rule so sticky
// routing stays stable throughout a multi-request turn.
func TestSessionTurnStateIsStableWithinTurn(t *testing.T) {
	t.Parallel()

	router, err := dexco.NewRouter(echoTool{})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &turnStateClient{t: t, stable: true}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "run two echoes"},
	}, dexco.NopSink{}); err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if len(client.prompts) != 3 {
		t.Fatalf("prompts = %d, want 3", len(client.prompts))
	}
}

// Adapted from Codex client request tests. Base instructions are prompt
// metadata for every model request, not a synthetic transcript item. That keeps
// history stable while still letting future Codex instruction updates flow into
// Dexco through Config.
type instructionRecordingClient struct {
	prompts []dexco.Prompt
}

func (c *instructionRecordingClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	item := dexco.AssistantMessageItem("ok")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

func TestSessionPassesConfiguredInstructionsToModelPrompt(t *testing.T) {
	t.Parallel()

	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &instructionRecordingClient{}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{Instructions: "Be precise."}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	for _, input := range []string{"first", "second"} {
		_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: input},
		}, dexco.NopSink{})
		if err != nil {
			t.Fatalf("SubmitUserInput(%q) error = %v", input, err)
		}
	}

	if len(client.prompts) != 2 {
		t.Fatalf("prompts = %d, want 2", len(client.prompts))
	}
	for index, prompt := range client.prompts {
		if prompt.Instructions != "Be precise." {
			t.Fatalf("prompt[%d].Instructions = %q, want Be precise.", index, prompt.Instructions)
		}
		if containsUserInput(prompt.History, "Be precise.") {
			t.Fatalf("prompt[%d] leaked instructions into history: %#v", index, prompt.History)
		}
	}
}

// Adapted from Codex core's json_result tests. Codex forwards
// final_output_json_schema as request metadata and surfaces the assistant JSON
// unchanged. Dexco exposes the same portable library invariant as
// OpUserInput.OutputSchema -> Prompt.OutputSchema; it must be present on every
// sampling request in that turn, must not become transcript history, and must
// not leak into later turns unless supplied again.
type schemaForwardingClient struct {
	t       *testing.T
	schema  json.RawMessage
	prompts []dexco.Prompt
	calls   int
}

func (c *schemaForwardingClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	c.calls++
	switch c.calls {
	case 1:
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "schema-call",
			Name:      "echo_tool",
			Arguments: json.RawMessage(`{"value":"schema-followup"}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 2:
		if !containsToolResult(prompt.History, "schema-call", "schema-followup") {
			c.t.Fatalf("schema follow-up prompt missing tool result: %#v", prompt.History)
		}
		item := dexco.AssistantMessageItem(`{"explanation":"ok","final_answer":"done"}`)
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 3:
		if len(prompt.OutputSchema) != 0 {
			c.t.Fatalf("next turn OutputSchema = %s, want empty", string(prompt.OutputSchema))
		}
		if containsAssistantMessage(prompt.History, string(c.schema)) {
			c.t.Fatalf("schema leaked into durable history: %#v", prompt.History)
		}
		item := dexco.AssistantMessageItem("plain next turn")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", c.calls)
		return nil, nil
	}
}

func TestSessionForwardsOutputSchemaToTurnPrompts(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"explanation": { "type": "string" },
			"final_answer": { "type": "string" }
		},
		"required": ["explanation", "final_answer"],
		"additionalProperties": false
	}`)
	router, err := dexco.NewRouter(echoTool{})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &schemaForwardingClient{t: t, schema: schema}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input:        dexco.UserInput{Content: "return json"},
		OutputSchema: schema,
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(schema turn) error = %v", err)
	}

	if result.FinalMessage != `{"explanation":"ok","final_answer":"done"}` {
		t.Fatalf("FinalMessage = %q, want JSON assistant message unchanged", result.FinalMessage)
	}
	if len(client.prompts) != 2 {
		t.Fatalf("prompts after schema turn = %d, want 2", len(client.prompts))
	}
	for index, prompt := range client.prompts {
		assertJSONRawEqual(t, prompt.OutputSchema, schema, "prompt output schema")
		if containsUserInput(prompt.History, string(schema)) {
			t.Fatalf("prompt[%d] leaked schema into user history: %#v", index, prompt.History)
		}
	}

	next, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "plain next turn"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(next turn) error = %v", err)
	}
	if next.FinalMessage != "plain next turn" {
		t.Fatalf("next FinalMessage = %q, want plain next turn", next.FinalMessage)
	}
}

// Adapted from Codex core's current_time_reminder tests. Dexco exposes a
// smaller library API, but preserves the important prompt invariant: current
// time reminders are developer prompt fragments, persist across same-turn tool
// follow-up requests, and are appended again only when the configured interval
// has elapsed.
type timeReminderClient struct {
	t       *testing.T
	prompts []dexco.Prompt
	calls   int
}

func (c *timeReminderClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	c.calls++
	switch c.calls {
	case 1:
		assertStringSliceEqual(c.t, prompt.DeveloperMessages, []string{
			"It is 2026-06-17 17:34:15 UTC.",
		}, "first prompt developer messages")
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "time-reminder-call",
			Name:      "echo_tool",
			Arguments: json.RawMessage(`{"value":"time-followup"}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 2:
		assertStringSliceEqual(c.t, prompt.DeveloperMessages, []string{
			"It is 2026-06-17 17:34:15 UTC.",
		}, "same-turn follow-up developer messages")
		if !containsToolResult(prompt.History, "time-reminder-call", "time-followup") {
			c.t.Fatalf("follow-up prompt missing tool result: %#v", prompt.History)
		}
		item := dexco.AssistantMessageItem("first done")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 3:
		assertStringSliceEqual(c.t, prompt.DeveloperMessages, []string{
			"It is 2026-06-17 17:34:15 UTC.",
		}, "second turn developer messages before interval")
		item := dexco.AssistantMessageItem("second done")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 4:
		assertStringSliceEqual(c.t, prompt.DeveloperMessages, []string{
			"It is 2026-06-17 17:34:15 UTC.",
			"It is 2026-06-17 17:36:15 UTC.",
		}, "third turn developer messages after interval")
		for _, reminder := range prompt.DeveloperMessages {
			if containsUserInput(prompt.History, reminder) || containsAssistantMessage(prompt.History, reminder) {
				c.t.Fatalf("time reminder leaked into durable history: %#v", prompt.History)
			}
		}
		item := dexco.AssistantMessageItem("third done")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", c.calls)
		return nil, nil
	}
}

func TestSessionInjectsCurrentTimeReminders(t *testing.T) {
	t.Parallel()

	times := []time.Time{
		time.Date(2026, 6, 17, 17, 34, 15, 0, time.UTC),
		time.Date(2026, 6, 17, 17, 35, 15, 0, time.UTC),
		time.Date(2026, 6, 17, 17, 36, 15, 0, time.UTC),
	}
	clock := &scriptedClock{times: times}
	router, err := dexco.NewRouter(echoTool{})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &timeReminderClient{t: t}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{
		TimeReminder: dexco.TimeReminderConfig{
			Clock:    clock.Now,
			Interval: 2 * time.Minute,
		},
	}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	for _, input := range []string{"first", "second", "third"} {
		_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: input},
		}, dexco.NopSink{})
		if err != nil {
			t.Fatalf("SubmitUserInput(%q) error = %v", input, err)
		}
	}

	if clock.calls != 3 {
		t.Fatalf("clock calls = %d, want 3", clock.calls)
	}
	if client.calls != 4 {
		t.Fatalf("model calls = %d, want 4", client.calls)
	}
}

// Adapted from Codex current_time_reminder's backward-time zero-interval case.
// A zero interval means "inject on every turn", even if the supplied clock moves
// backward.
func TestSessionCurrentTimeReminderZeroIntervalAllowsBackwardTime(t *testing.T) {
	t.Parallel()

	clock := &scriptedClock{
		times: []time.Time{
			time.Date(2026, 6, 17, 17, 34, 15, 0, time.UTC),
			time.Date(2026, 6, 17, 17, 33, 15, 0, time.UTC),
		},
	}
	client := &developerMessageRecordingClient{}
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{
		TimeReminder: dexco.TimeReminderConfig{
			Clock: clock.Now,
		},
	}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	for _, input := range []string{"first", "second"} {
		_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: input},
		}, dexco.NopSink{})
		if err != nil {
			t.Fatalf("SubmitUserInput(%q) error = %v", input, err)
		}
	}

	want := [][]string{
		{"It is 2026-06-17 17:34:15 UTC."},
		{
			"It is 2026-06-17 17:34:15 UTC.",
			"It is 2026-06-17 17:33:15 UTC.",
		},
	}
	if !reflect.DeepEqual(client.developerMessages, want) {
		t.Fatalf("developer messages = %#v, want %#v", client.developerMessages, want)
	}
}

type clockFailureSink struct {
	dexco.NopSink
	started bool
}

func (s *clockFailureSink) OnTurnStarted(context.Context, dexco.Turn) error {
	s.started = true
	return nil
}

// Adapted from Codex current_time_reminder failure coverage. If the configured
// clock cannot be read, Dexco stops before inference instead of sending a prompt
// with stale or missing time context.
func TestSessionCurrentTimeReminderFailureStopsBeforeSampling(t *testing.T) {
	t.Parallel()

	client := &instructionRecordingClient{}
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{
		TimeReminder: dexco.TimeReminderConfig{
			Clock: func(context.Context) (time.Time, error) {
				return time.Time{}, errors.New("test clock unavailable")
			},
			Interval: time.Minute,
		},
	}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	sink := &clockFailureSink{}
	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "fail before inference"},
	}, sink)
	if err == nil || err.Error() != "read current time reminder: test clock unavailable" {
		t.Fatalf("SubmitUserInput() error = %v, want clock failure", err)
	}
	if sink.started {
		t.Fatalf("turn started event emitted despite clock failure")
	}
	if client.prompts != nil {
		t.Fatalf("model prompts = %#v, want none", client.prompts)
	}
}

const (
	firstPermissionInstructions  = "<permissions instructions>approval policy: on request</permissions instructions>"
	secondPermissionInstructions = "<permissions instructions>approval policy: never</permissions instructions>"
	firstModelSwitchText         = "Use the next model's base instructions."
	secondModelSwitchText        = "Use the later model's base instructions."
	firstCollaborationText       = "collaborate actively"
	secondCollaborationText      = "work independently"
	firstStyleText               = "You optimize for team morale and being a supportive teammate as much as code quality."
	secondStyleText              = "You are a deeply pragmatic, effective software engineer."
)

// Adapted from Codex core's permissions_messages tests. Permission instructions
// are developer prompt fragments, not durable conversation history. Dexco keeps
// Codex's cache-friendly behavior: send the current text once, keep replaying
// the accumulated fragments, and append a new fragment only when the effective
// permissions text changes.
func TestSessionPermissionInstructionsSentOnceAndRefreshedOnChange(t *testing.T) {
	t.Parallel()

	client := &developerMessageRecordingClient{}
	session := newPermissionInstructionsTestSession(t, client, dexco.PermissionInstructionsConfig{
		Text: firstPermissionInstructions,
	})

	first, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "first"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(first) error = %v", err)
	}
	second, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "second"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(second) error = %v", err)
	}
	if err := session.SetPermissionInstructions(secondPermissionInstructions); err != nil {
		t.Fatalf("SetPermissionInstructions() error = %v", err)
	}
	third, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "third"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(third) error = %v", err)
	}

	want := [][]string{
		{firstPermissionInstructions},
		{firstPermissionInstructions},
		{firstPermissionInstructions, secondPermissionInstructions},
	}
	if !reflect.DeepEqual(client.developerMessages, want) {
		t.Fatalf("developer messages = %#v, want %#v", client.developerMessages, want)
	}
	for _, result := range []dexco.TurnResult{first, second, third} {
		for _, instructions := range []string{firstPermissionInstructions, secondPermissionInstructions} {
			if containsUserInput(result.History, instructions) || containsAssistantMessage(result.History, instructions) {
				t.Fatalf("permission instructions leaked into durable history: %#v", result.History)
			}
		}
	}
}

// Adapted from Codex permissions_messages disabled coverage. If the embedding
// application disables permissions instructions, Dexco must not send stale or
// configured text to the model.
func TestSessionPermissionInstructionsCanBeDisabled(t *testing.T) {
	t.Parallel()

	client := &developerMessageRecordingClient{}
	session := newPermissionInstructionsTestSession(t, client, dexco.PermissionInstructionsConfig{
		Text:     firstPermissionInstructions,
		Disabled: true,
	})

	_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "disabled"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}
	if len(client.developerMessages) != 1 || len(client.developerMessages[0]) != 0 {
		t.Fatalf("developer messages = %#v, want one empty prompt message list", client.developerMessages)
	}
}

func TestSessionPermissionInstructionsAreBounded(t *testing.T) {
	t.Parallel()

	client := &developerMessageRecordingClient{}
	longInstructions := "<permissions instructions>" + strings.Repeat("x", 5000) + "</permissions instructions>"
	session := newPermissionInstructionsTestSession(t, client, dexco.PermissionInstructionsConfig{
		Text: longInstructions,
	})

	_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "bounded"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}
	if len(client.developerMessages) != 1 || len(client.developerMessages[0]) != 1 {
		t.Fatalf("developer messages = %#v, want one bounded permission message", client.developerMessages)
	}
	message := client.developerMessages[0][0]
	if len([]rune(message)) > 4200 {
		t.Fatalf("permission instructions length = %d, want bounded", len([]rune(message)))
	}
	if !strings.Contains(message, "permission instructions truncated") {
		t.Fatalf("permission instructions missing truncation marker: %q", message)
	}
}

// Adapted from Codex model_switching tests. Dexco does not route provider model
// changes itself, but it preserves the portable prompt invariant: when callers
// switch models, the next model's instructions are replayed as a contextual
// developer `<model_switch>` fragment, append-only and de-duplicated.
func TestSessionModelSwitchInstructionsAppendOnChange(t *testing.T) {
	t.Parallel()

	client := &developerMessageRecordingClient{}
	session := newModelSwitchInstructionsTestSession(t, client, dexco.ModelSwitchInstructionsConfig{})

	first, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "first"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(first) error = %v", err)
	}
	if err := session.SetModelSwitchInstructions(firstModelSwitchText); err != nil {
		t.Fatalf("SetModelSwitchInstructions(first) error = %v", err)
	}
	second, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "second"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(second) error = %v", err)
	}
	if err := session.SetModelSwitchInstructions(firstModelSwitchText); err != nil {
		t.Fatalf("SetModelSwitchInstructions(noop) error = %v", err)
	}
	third, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "third"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(third) error = %v", err)
	}
	if err := session.SetModelSwitchInstructions(secondModelSwitchText); err != nil {
		t.Fatalf("SetModelSwitchInstructions(second) error = %v", err)
	}
	fourth, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "fourth"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(fourth) error = %v", err)
	}

	firstMessage := modelSwitchInstructions(firstModelSwitchText)
	secondMessage := modelSwitchInstructions(secondModelSwitchText)
	want := [][]string{
		nil,
		{firstMessage},
		{firstMessage},
		{firstMessage, secondMessage},
	}
	if !reflect.DeepEqual(client.developerMessages, want) {
		t.Fatalf("developer messages = %#v, want %#v", client.developerMessages, want)
	}
	for _, result := range []dexco.TurnResult{first, second, third, fourth} {
		for _, instructions := range []string{firstMessage, secondMessage} {
			if historyContainsContent(result.History, instructions) {
				t.Fatalf("model switch instructions leaked into durable history: %#v", result.History)
			}
		}
	}
}

// Mirrors Codex's `model_and_personality_change_only_appends_model_instructions`.
// If the model and style change before the same turn, the model-switch fragment
// is treated as the source of truth and the separate `<personality_spec>` update
// is suppressed to avoid redundant or contradictory guidance.
func TestSessionModelSwitchSuppressesSameTurnStyleUpdate(t *testing.T) {
	t.Parallel()

	client := &developerMessageRecordingClient{}
	session := newModelSwitchInstructionsTestSession(t, client, dexco.ModelSwitchInstructionsConfig{})
	_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "first"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(first) error = %v", err)
	}
	if err := session.SetStyleInstructions(firstStyleText); err != nil {
		t.Fatalf("SetStyleInstructions(first) error = %v", err)
	}
	if err := session.SetModelSwitchInstructions(firstModelSwitchText); err != nil {
		t.Fatalf("SetModelSwitchInstructions(first) error = %v", err)
	}
	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "switch model and style"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(second) error = %v", err)
	}
	if err := session.SetStyleInstructions(firstStyleText); err != nil {
		t.Fatalf("SetStyleInstructions(noop style) error = %v", err)
	}
	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "same style after switch"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(third) error = %v", err)
	}
	if err := session.SetStyleInstructions(secondStyleText); err != nil {
		t.Fatalf("SetStyleInstructions(second style) error = %v", err)
	}
	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "style only"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(fourth) error = %v", err)
	}

	modelMessage := modelSwitchInstructions(firstModelSwitchText)
	styleMessage := styleInstructions(secondStyleText)
	want := [][]string{
		nil,
		{modelMessage},
		{modelMessage},
		{modelMessage, styleMessage},
	}
	if !reflect.DeepEqual(client.developerMessages, want) {
		t.Fatalf("developer messages = %#v, want %#v", client.developerMessages, want)
	}
}

func TestSessionModelSwitchInstructionsAreBounded(t *testing.T) {
	t.Parallel()

	client := &developerMessageRecordingClient{}
	session := newModelSwitchInstructionsTestSession(t, client, dexco.ModelSwitchInstructionsConfig{
		Text: strings.Repeat("x", 5000),
	})

	_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "bounded"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}
	if len(client.developerMessages) != 1 || len(client.developerMessages[0]) != 1 {
		t.Fatalf("developer messages = %#v, want one bounded model-switch message", client.developerMessages)
	}
	message := client.developerMessages[0][0]
	if len([]rune(message)) > 4300 {
		t.Fatalf("model switch instructions length = %d, want bounded", len([]rune(message)))
	}
	if !strings.Contains(message, "<model_switch>") || !strings.Contains(message, "</model_switch>") {
		t.Fatalf("model switch instructions missing Codex markers: %q", message)
	}
	if !strings.Contains(message, "The user was previously using a different model.") {
		t.Fatalf("model switch instructions missing Codex preamble: %q", message)
	}
	if !strings.Contains(message, "model switch instructions truncated") {
		t.Fatalf("model switch instructions missing truncation marker: %q", message)
	}
}

// Adapted from Codex core's collaboration_instructions tests. Collaboration
// mode instructions are contextual developer prompt fragments: they are wrapped
// in Codex's `<collaboration_mode>` markers, replayed across turns, and append a
// new fragment only when the effective collaboration guidance changes.
func TestSessionCollaborationInstructionsSentOnceAndRefreshedOnChange(t *testing.T) {
	t.Parallel()

	client := &developerMessageRecordingClient{}
	session := newCollaborationInstructionsTestSession(t, client, dexco.CollaborationInstructionsConfig{
		Text: firstCollaborationText,
	})

	first, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "first"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(first) error = %v", err)
	}
	second, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "second"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(second) error = %v", err)
	}
	if err := session.SetCollaborationInstructions(secondCollaborationText); err != nil {
		t.Fatalf("SetCollaborationInstructions() error = %v", err)
	}
	third, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "third"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(third) error = %v", err)
	}
	if err := session.SetCollaborationInstructions(secondCollaborationText); err != nil {
		t.Fatalf("SetCollaborationInstructions(noop) error = %v", err)
	}
	fourth, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "fourth"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(fourth) error = %v", err)
	}

	firstMessage := collaborationInstructions(firstCollaborationText)
	secondMessage := collaborationInstructions(secondCollaborationText)
	want := [][]string{
		{firstMessage},
		{firstMessage},
		{firstMessage, secondMessage},
		{firstMessage, secondMessage},
	}
	if !reflect.DeepEqual(client.developerMessages, want) {
		t.Fatalf("developer messages = %#v, want %#v", client.developerMessages, want)
	}
	for _, result := range []dexco.TurnResult{first, second, third, fourth} {
		for _, instructions := range []string{firstMessage, secondMessage} {
			if containsUserInput(result.History, instructions) || containsAssistantMessage(result.History, instructions) {
				t.Fatalf("collaboration instructions leaked into durable history: %#v", result.History)
			}
		}
	}
}

// Mirrors Codex's no-collaboration-instructions-by-default and disabled
// coverage. Dexco should not invent collaboration mode prompt text unless the
// embedding application opts in.
func TestSessionCollaborationInstructionsCanBeDisabledOrUnset(t *testing.T) {
	t.Parallel()

	unsetClient := &developerMessageRecordingClient{}
	unsetSession := newCollaborationInstructionsTestSession(t, unsetClient, dexco.CollaborationInstructionsConfig{})
	_, err := unsetSession.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "unset"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(unset) error = %v", err)
	}
	if len(unsetClient.developerMessages) != 1 || len(unsetClient.developerMessages[0]) != 0 {
		t.Fatalf("unset developer messages = %#v, want one empty prompt message list", unsetClient.developerMessages)
	}

	disabledClient := &developerMessageRecordingClient{}
	disabledSession := newCollaborationInstructionsTestSession(t, disabledClient, dexco.CollaborationInstructionsConfig{
		Text:     firstCollaborationText,
		Disabled: true,
	})
	_, err = disabledSession.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "disabled"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(disabled) error = %v", err)
	}
	if len(disabledClient.developerMessages) != 1 || len(disabledClient.developerMessages[0]) != 0 {
		t.Fatalf("disabled developer messages = %#v, want one empty prompt message list", disabledClient.developerMessages)
	}
}

// Codex ignores empty CollaborationMode developer_instructions updates instead
// of appending an empty `<collaboration_mode></collaboration_mode>` fragment.
func TestSessionEmptyCollaborationInstructionsAreIgnored(t *testing.T) {
	t.Parallel()

	client := &developerMessageRecordingClient{}
	session := newCollaborationInstructionsTestSession(t, client, dexco.CollaborationInstructionsConfig{
		Text: firstCollaborationText,
	})

	_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "first"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(first) error = %v", err)
	}
	if err := session.SetCollaborationInstructions(""); err != nil {
		t.Fatalf("SetCollaborationInstructions(empty) error = %v", err)
	}
	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "second"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(second) error = %v", err)
	}

	want := [][]string{
		{collaborationInstructions(firstCollaborationText)},
		{collaborationInstructions(firstCollaborationText)},
	}
	if !reflect.DeepEqual(client.developerMessages, want) {
		t.Fatalf("developer messages = %#v, want %#v", client.developerMessages, want)
	}
	if strings.Contains(strings.Join(client.developerMessages[1], "\n"), collaborationInstructions("")) {
		t.Fatalf("empty collaboration instructions were emitted: %#v", client.developerMessages[1])
	}
}

func TestSessionCollaborationInstructionsAreBounded(t *testing.T) {
	t.Parallel()

	client := &developerMessageRecordingClient{}
	session := newCollaborationInstructionsTestSession(t, client, dexco.CollaborationInstructionsConfig{
		Text: strings.Repeat("x", 5000),
	})

	_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "bounded"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}
	if len(client.developerMessages) != 1 || len(client.developerMessages[0]) != 1 {
		t.Fatalf("developer messages = %#v, want one bounded collaboration message", client.developerMessages)
	}
	message := client.developerMessages[0][0]
	if len([]rune(message)) > 4200 {
		t.Fatalf("collaboration instructions length = %d, want bounded", len([]rune(message)))
	}
	if !strings.Contains(message, "<collaboration_mode>") || !strings.Contains(message, "</collaboration_mode>") {
		t.Fatalf("collaboration instructions missing Codex markers: %q", message)
	}
	if !strings.Contains(message, "collaboration instructions truncated") {
		t.Fatalf("collaboration instructions missing truncation marker: %q", message)
	}
}

// Adapted from Codex personality tests for configured personality. Dexco does
// not own Codex's model registry or personality template expansion; callers
// carry initial/default style through base Instructions, which must not create a
// separate `<personality_spec>` developer update.
func TestSessionConfiguredInstructionsCarryInitialStyleWithoutPersonalitySpec(t *testing.T) {
	t.Parallel()

	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &instructionRecordingClient{}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	instructions := "Base instructions\n" + firstStyleText
	session, err := dexco.NewSession(dexco.Config{Instructions: instructions}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "initial style"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}
	if len(client.prompts) != 1 {
		t.Fatalf("prompts = %d, want 1", len(client.prompts))
	}
	prompt := client.prompts[0]
	if prompt.Instructions != instructions {
		t.Fatalf("Instructions = %q, want %q", prompt.Instructions, instructions)
	}
	if len(prompt.DeveloperMessages) != 0 {
		t.Fatalf("DeveloperMessages = %#v, want none", prompt.DeveloperMessages)
	}
}

// Adapted from Codex personality update tests. Runtime style changes are
// contextual developer fragments wrapped in `<personality_spec>`, appended only
// when the effective style changes, and kept out of durable chat history.
func TestSessionStyleInstructionsAppendPersonalitySpecOnChange(t *testing.T) {
	t.Parallel()

	client := &developerMessageRecordingClient{}
	session := newStyleInstructionsTestSession(t, client, dexco.StyleInstructionsConfig{})

	first, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "first"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(first) error = %v", err)
	}
	if err := session.SetStyleInstructions(firstStyleText); err != nil {
		t.Fatalf("SetStyleInstructions(first) error = %v", err)
	}
	second, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "second"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(second) error = %v", err)
	}
	if err := session.SetStyleInstructions(firstStyleText); err != nil {
		t.Fatalf("SetStyleInstructions(noop) error = %v", err)
	}
	third, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "third"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(third) error = %v", err)
	}
	if err := session.SetStyleInstructions(secondStyleText); err != nil {
		t.Fatalf("SetStyleInstructions(second) error = %v", err)
	}
	fourth, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "fourth"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(fourth) error = %v", err)
	}

	firstMessage := styleInstructions(firstStyleText)
	secondMessage := styleInstructions(secondStyleText)
	want := [][]string{
		nil,
		{firstMessage},
		{firstMessage},
		{firstMessage, secondMessage},
	}
	if !reflect.DeepEqual(client.developerMessages, want) {
		t.Fatalf("developer messages = %#v, want %#v", client.developerMessages, want)
	}
	for _, result := range []dexco.TurnResult{first, second, third, fourth} {
		for _, instructions := range []string{firstMessage, secondMessage} {
			if historyContainsContent(result.History, instructions) {
				t.Fatalf("style instructions leaked into durable history: %#v", result.History)
			}
		}
	}
}

func TestSessionStyleInstructionsCanBeDisabledOrUnset(t *testing.T) {
	t.Parallel()

	unsetClient := &developerMessageRecordingClient{}
	unsetSession := newStyleInstructionsTestSession(t, unsetClient, dexco.StyleInstructionsConfig{})
	_, err := unsetSession.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "unset"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(unset) error = %v", err)
	}
	if len(unsetClient.developerMessages) != 1 || len(unsetClient.developerMessages[0]) != 0 {
		t.Fatalf("unset developer messages = %#v, want one empty prompt message list", unsetClient.developerMessages)
	}

	disabledClient := &developerMessageRecordingClient{}
	disabledSession := newStyleInstructionsTestSession(t, disabledClient, dexco.StyleInstructionsConfig{
		Text:     firstStyleText,
		Disabled: true,
	})
	_, err = disabledSession.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "disabled"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(disabled) error = %v", err)
	}
	if len(disabledClient.developerMessages) != 1 || len(disabledClient.developerMessages[0]) != 0 {
		t.Fatalf("disabled developer messages = %#v, want one empty prompt message list", disabledClient.developerMessages)
	}
}

func TestSessionEmptyStyleInstructionsAreIgnored(t *testing.T) {
	t.Parallel()

	client := &developerMessageRecordingClient{}
	session := newStyleInstructionsTestSession(t, client, dexco.StyleInstructionsConfig{
		Text: firstStyleText,
	})

	_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "first"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(first) error = %v", err)
	}
	if err := session.SetStyleInstructions(""); err != nil {
		t.Fatalf("SetStyleInstructions(empty) error = %v", err)
	}
	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "second"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(second) error = %v", err)
	}

	want := [][]string{
		{styleInstructions(firstStyleText)},
		{styleInstructions(firstStyleText)},
	}
	if !reflect.DeepEqual(client.developerMessages, want) {
		t.Fatalf("developer messages = %#v, want %#v", client.developerMessages, want)
	}
	if strings.Contains(strings.Join(client.developerMessages[1], "\n"), styleInstructions("")) {
		t.Fatalf("empty style instructions were emitted: %#v", client.developerMessages[1])
	}
}

func TestSessionStyleInstructionsAreBounded(t *testing.T) {
	t.Parallel()

	client := &developerMessageRecordingClient{}
	session := newStyleInstructionsTestSession(t, client, dexco.StyleInstructionsConfig{
		Text: strings.Repeat("x", 5000),
	})

	_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "bounded"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}
	if len(client.developerMessages) != 1 || len(client.developerMessages[0]) != 1 {
		t.Fatalf("developer messages = %#v, want one bounded style message", client.developerMessages)
	}
	message := client.developerMessages[0][0]
	if len([]rune(message)) > 4300 {
		t.Fatalf("style instructions length = %d, want bounded", len([]rune(message)))
	}
	if !strings.Contains(message, "<personality_spec>") || !strings.Contains(message, "</personality_spec>") {
		t.Fatalf("style instructions missing Codex markers: %q", message)
	}
	if !strings.Contains(message, "The user has requested a new communication style.") {
		t.Fatalf("style instructions missing Codex preamble: %q", message)
	}
	if !strings.Contains(message, "personality instructions truncated") {
		t.Fatalf("style instructions missing truncation marker: %q", message)
	}
}

type scriptedClock struct {
	times []time.Time
	calls int
}

func (c *scriptedClock) Now(context.Context) (time.Time, error) {
	if c.calls >= len(c.times) {
		return time.Time{}, fmt.Errorf("unexpected clock call %d", c.calls+1)
	}
	value := c.times[c.calls]
	c.calls++
	return value, nil
}

type developerMessageRecordingClient struct {
	developerMessages [][]string
}

func (c *developerMessageRecordingClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.developerMessages = append(c.developerMessages, append([]string(nil), prompt.DeveloperMessages...))
	item := dexco.AssistantMessageItem("ok")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

type blockingStartClient struct {
	streamStarted chan struct{}
	releaseStream chan struct{}
}

func (c *blockingStartClient) Stream(context.Context, dexco.Prompt) (dexco.EventStream, error) {
	close(c.streamStarted)
	<-c.releaseStream
	item := dexco.AssistantMessageItem("started")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

type turnStartOrderSink struct {
	dexco.NopSink
	started       chan struct{}
	clientStarted chan struct{}
	releaseClient chan struct{}
}

func (s *turnStartOrderSink) OnTurnStarted(context.Context, dexco.Turn) error {
	close(s.started)
	return nil
}

func (s *turnStartOrderSink) OnClientEvent(_ context.Context, event dexco.ClientEvent) error {
	if event.Type == dexco.ClientEventTurnStarted {
		close(s.clientStarted)
		<-s.releaseClient
	}
	return nil
}

// Adapted from Codex session startup tests. Turn-start notifications must be
// delivered before slow sampling work begins so callers can render a live turn
// immediately even if model startup/prewarm blocks.
func TestSessionEmitsTurnStartedBeforeSampling(t *testing.T) {
	t.Parallel()

	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &blockingStartClient{
		streamStarted: make(chan struct{}),
		releaseStream: make(chan struct{}),
	}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	sink := &turnStartOrderSink{
		started:       make(chan struct{}),
		clientStarted: make(chan struct{}),
		releaseClient: make(chan struct{}),
	}

	type resultErr struct {
		err error
	}
	done := make(chan resultErr, 1)
	go func() {
		_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: "start"},
		}, sink)
		done <- resultErr{err: err}
	}()

	waitForClosed(t, sink.started, "turn started")
	waitForClosed(t, sink.clientStarted, "client turn started")
	assertStillOpen(t, client.streamStarted, "model stream started before turn-start events completed")

	close(sink.releaseClient)
	waitForClosed(t, client.streamStarted, "model stream started")
	close(client.releaseStream)

	outcome := <-done
	if outcome.err != nil {
		t.Fatalf("SubmitUserInput() error = %v", outcome.err)
	}
}

func TestDefaultRouterExposesBuiltinToolSpecs(t *testing.T) {
	t.Parallel()

	router, err := dexco.NewDefaultRouter(func(context.Context, string) (string, error) {
		return "answer", nil
	})
	if err != nil {
		t.Fatalf("NewDefaultRouter() error = %v", err)
	}

	var names []string
	for _, spec := range router.Specs() {
		names = append(names, spec.Name)
	}

	want := []string{"current_time", "exec_command", "request_user_input", "update_plan", "view_image"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("tool names = %#v, want %#v", names, want)
	}
}

// Adapted from Codex core's update_plan tool harness tests. The model-visible
// output is the fixed "Plan updated" string, while clients receive a richer
// PlanUpdate event with explanation and checklist state.
type updatePlanLoopClient struct {
	t     *testing.T
	calls int
}

func (c *updatePlanLoopClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	if c.calls == 1 {
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID: "plan-call",
			Name:   "update_plan",
			Arguments: json.RawMessage(`{
				"explanation": "Tracking implementation",
				"plan": [{
					"step": "Implement handler",
					"status": "completed"
				}, {
					"step": "Run tests",
					"status": "in_progress"
				}]
			}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	}
	if !containsToolResult(prompt.History, "plan-call", "Plan updated") {
		c.t.Fatalf("second prompt missing update_plan tool result: %#v", prompt.History)
	}
	if hasToolResultPlanUpdate(prompt.History, "plan-call") {
		c.t.Fatalf("second prompt leaked plan metadata into model-visible history: %#v", prompt.History)
	}
	item := dexco.AssistantMessageItem("plan recorded")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

func TestDefaultSessionRunsUpdatePlanRoundTrip(t *testing.T) {
	t.Parallel()

	session, err := dexco.NewDefaultSession(
		dexco.Config{},
		&updatePlanLoopClient{t: t},
		nil,
	)
	if err != nil {
		t.Fatalf("NewDefaultSession() error = %v", err)
	}

	sink := &clientEventSink{}
	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "track the plan"},
	}, sink)
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "plan recorded" {
		t.Fatalf("FinalMessage = %q, want plan recorded", result.FinalMessage)
	}
	planIndex := clientEventIndex(sink.events, dexco.ClientEventPlanUpdate)
	toolResultIndex := clientEventIndex(sink.events, dexco.ClientEventToolResult)
	if planIndex == -1 || toolResultIndex == -1 || planIndex > toolResultIndex {
		t.Fatalf("client events = %#v, want plan_update before tool_result", clientEventTypes(sink.events))
	}
	want := dexco.PlanUpdate{
		Explanation: "Tracking implementation",
		Plan: []dexco.PlanStep{
			{Step: "Implement handler", Status: dexco.PlanStepCompleted},
			{Step: "Run tests", Status: dexco.PlanStepInProgress},
		},
	}
	if got := sink.events[planIndex].PlanUpdate; got == nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("PlanUpdate = %#v, want %#v", got, want)
	}
	if got := sink.events[toolResultIndex].ToolResult; got == nil || got.PlanUpdate != nil {
		t.Fatalf("tool_result event PlanUpdate = %#v, want nil", got)
	}
}

// Adapted from Codex core's malformed update_plan test. Invalid payloads should
// be visible to the next model request as a failed tool output, but must not
// emit a structured PlanUpdate event because clients should not render invalid
// checklist state.
type malformedUpdatePlanLoopClient struct {
	t     *testing.T
	calls int
}

func (c *malformedUpdatePlanLoopClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	if c.calls == 1 {
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "bad-plan-call",
			Name:      "update_plan",
			Arguments: json.RawMessage(`{"explanation":"missing required plan"}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	}
	if !containsToolResultContaining(prompt.History, "bad-plan-call", "failed to parse function arguments") {
		c.t.Fatalf("second prompt missing malformed update_plan output: %#v", prompt.History)
	}
	item := dexco.AssistantMessageItem("bad plan handled")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

func TestDefaultSessionRejectsMalformedUpdatePlanWithoutPlanEvent(t *testing.T) {
	t.Parallel()

	session, err := dexco.NewDefaultSession(
		dexco.Config{},
		&malformedUpdatePlanLoopClient{t: t},
		nil,
	)
	if err != nil {
		t.Fatalf("NewDefaultSession() error = %v", err)
	}

	sink := &clientEventSink{}
	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "track the plan"},
	}, sink)
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "bad plan handled" {
		t.Fatalf("FinalMessage = %q, want bad plan handled", result.FinalMessage)
	}
	if hasClientEvent(sink.events, dexco.ClientEventPlanUpdate) {
		t.Fatalf("client events include unexpected plan_update: %#v", clientEventTypes(sink.events))
	}
}

type deferredToolSearchClient struct {
	t     *testing.T
	calls int
}

func (c *deferredToolSearchClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	wantTools := []string{"direct_notes", "tool_search"}
	if got := promptToolNames(prompt.Tools); !reflect.DeepEqual(got, wantTools) {
		c.t.Fatalf("prompt[%d] tools = %#v, want %#v", c.calls, got, wantTools)
	}
	switch c.calls {
	case 1:
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "search-call",
			Name:      "tool_search",
			Arguments: json.RawMessage(`{"query":"forecast","limit":1}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 2:
		if !containsToolResultContaining(prompt.History, "search-call", `"name":"hidden_weather"`) {
			c.t.Fatalf("second prompt missing hidden tool_search descriptor: %#v", prompt.History)
		}
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "hidden-call",
			Name:      "hidden_weather",
			Arguments: json.RawMessage(`{"city":"Paris"}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 3:
		if !containsToolResult(prompt.History, "hidden-call", "forecast for Paris") {
			c.t.Fatalf("third prompt missing hidden tool output: %#v", prompt.History)
		}
		item := dexco.AssistantMessageItem("deferred tool done")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", c.calls)
		return nil, nil
	}
}

// Adapted from Codex search_tool tests. Deferred tools are registered for
// dispatch but omitted from initial tool specs; `tool_search` returns
// model-visible loadable descriptors, and follow-up calls route by exact hidden
// tool name without injecting discovered tools into Prompt.Tools.
func TestSessionDeferredToolSearchFindsHiddenToolWithoutAdvertisingIt(t *testing.T) {
	t.Parallel()

	router, err := dexco.NewRouter(namedOutputTool{
		name:        "direct_notes",
		description: "visible note tool",
		output:      "notes",
	})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	if err := router.RegisterDeferred(weatherTool{}, "weather forecast rain temperature city"); err != nil {
		t.Fatalf("RegisterDeferred() error = %v", err)
	}
	client := &deferredToolSearchClient{t: t}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "find forecast tool"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}
	if result.FinalMessage != "deferred tool done" {
		t.Fatalf("FinalMessage = %q, want deferred tool done", result.FinalMessage)
	}
	if result.ModelCalls != 3 {
		t.Fatalf("ModelCalls = %d, want 3", result.ModelCalls)
	}
}

type namedOutputTool struct {
	name        string
	description string
	output      string
}

func (t namedOutputTool) Name() string {
	return t.name
}

func (t namedOutputTool) Spec() dexco.ToolSpec {
	return dexco.ToolSpec{
		Name:        t.name,
		Description: t.description,
	}
}

func (t namedOutputTool) Call(context.Context, dexco.ToolCall) (dexco.ToolResult, error) {
	return dexco.ToolResult{
		Output:  t.output,
		Success: true,
	}, nil
}

type weatherTool struct{}

func (weatherTool) Name() string {
	return "hidden_weather"
}

func (weatherTool) Spec() dexco.ToolSpec {
	return dexco.ToolSpec{
		Name:        "hidden_weather",
		Description: "Gets a city weather forecast.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{
					"type":        "string",
					"description": "City name.",
				},
			},
			"required": []string{"city"},
		},
	}
}

func (weatherTool) Call(_ context.Context, call dexco.ToolCall) (dexco.ToolResult, error) {
	var args struct {
		City string `json:"city"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return dexco.ToolResult{}, err
	}
	return dexco.ToolResult{
		Output:  "forecast for " + args.City,
		Success: true,
	}, nil
}

// Adapted from Codex core's request_user_input round-trip suite. Dexco keeps a
// smaller request shape, but the invariant is the same: a model tool call pauses
// for caller-provided input, appends a tool result, and resumes sampling with
// that result in history.
type requestUserInputLoopClient struct {
	t     *testing.T
	calls int
}

func (c *requestUserInputLoopClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	if c.calls == 1 {
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "user-input-call",
			Name:      "request_user_input",
			Arguments: json.RawMessage(`{"question":"Proceed with the plan?"}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	}
	if !containsToolResult(prompt.History, "user-input-call", "yes") {
		c.t.Fatalf("second prompt missing request_user_input result: %#v", prompt.History)
	}
	item := dexco.AssistantMessageItem("thanks")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

func TestDefaultSessionRunsRequestUserInputRoundTrip(t *testing.T) {
	t.Parallel()

	var prompted string
	session, err := dexco.NewDefaultSession(
		dexco.Config{},
		&requestUserInputLoopClient{t: t},
		func(_ context.Context, prompt string) (string, error) {
			prompted = prompt
			return "yes", nil
		},
	)
	if err != nil {
		t.Fatalf("NewDefaultSession() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "please confirm"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if prompted != "Proceed with the plan?" {
		t.Fatalf("prompted = %q, want Proceed with the plan?", prompted)
	}
	if result.FinalMessage != "thanks" {
		t.Fatalf("FinalMessage = %q, want thanks", result.FinalMessage)
	}
}

// Adapted from Codex core's richer request_user_input payload coverage. Dexco
// can still use its simple responder callback, but when the model emits Codex's
// structured questions array the tool result must use the same JSON answer map
// so the follow-up model request sees a Codex-compatible transcript item.
type structuredRequestUserInputLoopClient struct {
	t     *testing.T
	calls int
}

func (c *structuredRequestUserInputLoopClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	if c.calls == 1 {
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID: "structured-user-input-call",
			Name:   "request_user_input",
			Arguments: json.RawMessage(`{
				"questions": [{
					"id": "confirm_path",
					"header": "Confirm",
					"question": "Proceed with the plan?",
					"options": [{
						"label": "Yes (Recommended)",
						"description": "Continue the current plan."
					}, {
						"label": "No",
						"description": "Stop and revisit the approach."
					}]
				}],
				"autoResolutionMs": 60000
			}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	}
	wantOutput := `{"answers":{"confirm_path":{"answers":["yes"]}}}`
	if !containsToolResult(prompt.History, "structured-user-input-call", wantOutput) {
		c.t.Fatalf("second prompt missing structured request_user_input result: %#v", prompt.History)
	}
	item := dexco.AssistantMessageItem("structured thanks")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

func TestDefaultSessionRunsStructuredRequestUserInputRoundTrip(t *testing.T) {
	t.Parallel()

	var prompted string
	session, err := dexco.NewDefaultSession(
		dexco.Config{},
		&structuredRequestUserInputLoopClient{t: t},
		func(_ context.Context, prompt string) (string, error) {
			prompted = prompt
			return "yes", nil
		},
	)
	if err != nil {
		t.Fatalf("NewDefaultSession() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "please confirm"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if prompted != "Proceed with the plan?" {
		t.Fatalf("prompted = %q, want Proceed with the plan?", prompted)
	}
	if result.FinalMessage != "structured thanks" {
		t.Fatalf("FinalMessage = %q, want structured thanks", result.FinalMessage)
	}
}

// Adapted from Codex core's view_image tool round-trip tests. Dexco keeps the
// image content in ToolResult.Parts rather than serializing an HTTP Responses
// content array, but the loop invariant is the same: a local image tool call
// becomes a model-visible image result on the follow-up prompt.
type viewImageLoopClient struct {
	t     *testing.T
	calls int
}

func (c *viewImageLoopClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	if c.calls == 1 {
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "view-image-call",
			Name:      "view_image",
			Arguments: json.RawMessage(`{"path":"example.png"}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	}
	if !containsImageToolResult(prompt.History, "view-image-call") {
		c.t.Fatalf("second prompt missing view_image content part: %#v", prompt.History)
	}
	item := dexco.AssistantMessageItem("image attached")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

func TestDefaultSessionRunsViewImageToolRoundTrip(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	writeTinyPNG(t, filepath.Join(baseDir, "example.png"))
	router, err := dexco.NewRouter(dexco.ViewImageHandler{BaseDir: baseDir})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(&viewImageLoopClient{t: t}, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "attach image"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "image attached" {
		t.Fatalf("FinalMessage = %q, want image attached", result.FinalMessage)
	}
}

type userImagePromptClient struct {
	t            *testing.T
	wantPath     string
	wantBounds   image.Point
	wantDetail   string
	wantTextOnly bool
}

func (c *userImagePromptClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	if len(prompt.History) != 1 {
		c.t.Fatalf("History = %#v, want one user input item", prompt.History)
	}
	item := prompt.History[0]
	if item.Kind != dexco.ItemUserInput {
		c.t.Fatalf("History[0].Kind = %q, want user_input", item.Kind)
	}
	if c.wantTextOnly {
		if len(item.Parts) != 1 || item.Parts[0].Kind != dexco.ContentPartText {
			c.t.Fatalf("Parts = %#v, want one text placeholder", item.Parts)
		}
		if !strings.Contains(item.Parts[0].Text, c.wantPath) ||
			!strings.Contains(item.Parts[0].Text, "unsupported image `application/json`") {
			c.t.Fatalf("placeholder = %q, want unsupported JSON placeholder for %q", item.Parts[0].Text, c.wantPath)
		}
	} else {
		if item.Content != "attach user image" {
			c.t.Fatalf("Content = %q, want attach user image", item.Content)
		}
		if len(item.Parts) != 3 {
			c.t.Fatalf("Parts = %#v, want image tag, image, closing tag", item.Parts)
		}
		if !strings.Contains(item.Parts[0].Text, c.wantPath) {
			c.t.Fatalf("opening tag = %q, want path %q", item.Parts[0].Text, c.wantPath)
		}
		imagePart := item.Parts[1]
		if imagePart.Kind != dexco.ContentPartImage {
			c.t.Fatalf("image part = %#v, want image", imagePart)
		}
		if imagePart.Detail != c.wantDetail {
			c.t.Fatalf("Detail = %q, want %q", imagePart.Detail, c.wantDetail)
		}
		if got := decodeDexcoImageBounds(c.t, imagePart); got != c.wantBounds {
			c.t.Fatalf("image bounds = %v, want %v", got, c.wantBounds)
		}
		if item.Parts[2].Text != "</image>" {
			c.t.Fatalf("closing tag = %q, want </image>", item.Parts[2].Text)
		}
	}

	message := dexco.AssistantMessageItem("image prompt observed")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &message},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

// Adapted from Codex core's user-turn local-image tests. Dexco does not own
// Codex's HTTP request serializer, so it exposes the same prepared image content
// in Prompt.History for library model clients.
func TestSessionPreparesUserTurnLocalImageParts(t *testing.T) {
	t.Parallel()

	path := writeSizedPNG(t, image.Pt(2304, 864))
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(&userImagePromptClient{
		t:          t,
		wantPath:   path,
		wantBounds: image.Pt(2048, 768),
		wantDetail: "high",
	}, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{
			Content: "attach user image",
			Parts: []dexco.ContentPart{
				{Kind: dexco.ContentPartImage, Path: path},
			},
		},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}
	if result.FinalMessage != "image prompt observed" {
		t.Fatalf("FinalMessage = %q, want image prompt observed", result.FinalMessage)
	}
}

// Codex converts invalid local image input into bounded placeholder text rather
// than failing the turn. Dexco preserves that nuance at the library boundary so
// callers can decide how to render or log the placeholder.
func TestSessionUserTurnInvalidLocalImageBecomesPlaceholder(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "example.json")
	if err := os.WriteFile(path, []byte(`{"hello":"world"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(&userImagePromptClient{
		t:            t,
		wantPath:     path,
		wantTextOnly: true,
	}, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{
			Parts: []dexco.ContentPart{
				{Kind: dexco.ContentPartImage, Path: path},
			},
		},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}
}

type modelVisibleLayoutClient struct {
	prompts []dexco.Prompt
	calls   int
}

func (c *modelVisibleLayoutClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	c.calls++
	message := dexco.AssistantMessageItem(fmt.Sprintf("turn %d complete", c.calls))
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &message},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

// Adapted from Codex core's model_visible_layout snapshots. Dexco does not own
// Codex's Responses wire serializer, environment block, or snapshot renderer,
// but it must preserve the portable prompt layout: previous model-visible
// history first, contextual updates at the next turn boundary, then the new
// user input. Contextual developer fragments are replayed in the order they
// became visible rather than regrouped by type.
func TestSessionModelVisibleLayoutOrdersHistoryContextAndDeveloperUpdates(t *testing.T) {
	t.Parallel()

	client := &modelVisibleLayoutClient{}
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{
		PermissionInstructions: dexco.PermissionInstructionsConfig{
			Text: firstPermissionInstructions,
		},
		CollaborationInstructions: dexco.CollaborationInstructionsConfig{
			Text: firstCollaborationText,
		},
	}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "first turn"},
		AdditionalContext: map[string]dexco.AdditionalContextEntry{
			"automation_info": {
				Value: "run one",
				Kind:  dexco.AdditionalContextApplication,
			},
			"browser_info": {
				Value: "tab one",
				Kind:  dexco.AdditionalContextUntrusted,
			},
		},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(first) error = %v", err)
	}
	if err := session.SetPermissionInstructions(secondPermissionInstructions); err != nil {
		t.Fatalf("SetPermissionInstructions() error = %v", err)
	}
	if err := session.SetCollaborationInstructions(secondCollaborationText); err != nil {
		t.Fatalf("SetCollaborationInstructions() error = %v", err)
	}
	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "second turn with context updates"},
		AdditionalContext: map[string]dexco.AdditionalContextEntry{
			"automation_info": {
				Value: "run two",
				Kind:  dexco.AdditionalContextApplication,
			},
			"terminal_info": {
				Value: "pty one",
				Kind:  dexco.AdditionalContextUntrusted,
			},
		},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(second) error = %v", err)
	}

	if len(client.prompts) != 2 {
		t.Fatalf("prompts = %d, want 2", len(client.prompts))
	}
	assertStringSliceEqual(t, client.prompts[0].DeveloperMessages, []string{
		firstPermissionInstructions,
		collaborationInstructions(firstCollaborationText),
	}, "first prompt developer messages")
	assertStringSliceEqual(t, client.prompts[1].DeveloperMessages, []string{
		firstPermissionInstructions,
		collaborationInstructions(firstCollaborationText),
		secondPermissionInstructions,
		collaborationInstructions(secondCollaborationText),
	}, "second prompt developer messages")
	assertStringSliceEqual(t, itemLayout(client.prompts[0].History), []string{
		"context:developer:<automation_info>run one</automation_info>",
		"context:user:<external_browser_info>tab one</external_browser_info>",
		"user:first turn",
	}, "first prompt history layout")
	assertStringSliceEqual(t, itemLayout(client.prompts[1].History), []string{
		"context:developer:<automation_info>run one</automation_info>",
		"context:user:<external_browser_info>tab one</external_browser_info>",
		"user:first turn",
		"assistant:turn 1 complete",
		"context:developer:<automation_info>run two</automation_info>",
		"context:user:<external_terminal_info>pty one</external_terminal_info>",
		"user:second turn with context updates",
	}, "second prompt history layout")
	for _, prompt := range client.prompts {
		for _, developerMessage := range prompt.DeveloperMessages {
			if historyContainsContent(prompt.History, developerMessage) {
				t.Fatalf("developer message leaked into prompt history: %q in %#v", developerMessage, prompt.History)
			}
		}
	}
}

// Adapted from Codex core's user_shell_cmd history tests. Dexco does not own
// the user-facing shell executor, but embedders can record a completed
// user-initiated command as Codex-shaped contextual user history so the next
// model request sees exactly what the user ran and what happened.
func TestSessionRecordsUserShellCommandHistoryForNextTurn(t *testing.T) {
	t.Parallel()

	client := &additionalContextClient{t: t}
	session := newContextTestSession(t, client)
	_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "before shell"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(first) error = %v", err)
	}
	err = session.RecordUserShellCommand(dexco.UserShellCommandRecord{
		Command:  "echo hi",
		ExitCode: 0,
		Duration: time.Second,
		Output:   "hi",
	})
	if err != nil {
		t.Fatalf("RecordUserShellCommand() error = %v", err)
	}
	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "after shell"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(second) error = %v", err)
	}

	wantRecord := "<user_shell_command>\n<command>\necho hi\n</command>\n<result>\nExit code: 0\nDuration: 1.0000 seconds\nOutput:\nhi\n</result>\n</user_shell_command>"
	assertStringSliceEqual(t, itemLayout(client.prompts[1].History), []string{
		"user:before shell",
		"assistant:context observed",
		"context:user:" + wantRecord,
		"user:after shell",
	}, "second prompt history layout")
	if historyContainsContent(client.prompts[0].History, wantRecord) {
		t.Fatalf("shell record appeared before it was recorded: %#v", client.prompts[0].History)
	}
}

func TestSessionUserShellCommandOutputIsBoundedAndNotDoubleTruncated(t *testing.T) {
	t.Parallel()

	client := &additionalContextClient{t: t}
	session := newContextTestSession(t, client)
	longOutput := "head-" + strings.Repeat("x", 400) + "-tail"
	err := session.RecordUserShellCommand(dexco.UserShellCommandRecord{
		Command:        "large-output",
		ExitCode:       0,
		Duration:       120 * time.Millisecond,
		Output:         longOutput,
		MaxOutputChars: 80,
	})
	if err != nil {
		t.Fatalf("RecordUserShellCommand(large) error = %v", err)
	}
	alreadyTruncated := "Warning: truncated output (original character count: 500)\n\nfirst\n... 450 characters truncated ...\nlast"
	err = session.RecordUserShellCommand(dexco.UserShellCommandRecord{
		Command:        "already-truncated",
		ExitCode:       1,
		Duration:       time.Second,
		Output:         alreadyTruncated,
		MaxOutputChars: 20,
	})
	if err != nil {
		t.Fatalf("RecordUserShellCommand(already truncated) error = %v", err)
	}
	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "inspect shell records"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	records := contextContents(client.prompts[0].History, "user")
	if len(records) != 2 {
		t.Fatalf("user context records = %#v, want two shell records", records)
	}
	if !strings.Contains(records[0], "Warning: truncated output") ||
		!strings.Contains(records[0], "characters truncated") ||
		!strings.Contains(records[0], "head-") ||
		!strings.Contains(records[0], "-tail") {
		t.Fatalf("first shell record missing bounded truncation details: %q", records[0])
	}
	if len([]rune(records[0])) > 360 {
		t.Fatalf("first shell record length = %d, want bounded", len([]rune(records[0])))
	}
	if count := strings.Count(records[1], "Warning: truncated output"); count != 1 {
		t.Fatalf("already-truncated shell record has %d truncation warnings: %q", count, records[1])
	}
	if count := strings.Count(records[1], "characters truncated"); count != 1 {
		t.Fatalf("already-truncated shell record has %d truncation markers: %q", count, records[1])
	}
}

type activeUserShellRecordClient struct {
	t          *testing.T
	firstDelta chan struct{}
	release    chan struct{}
	calls      int
}

func (c *activeUserShellRecordClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	switch c.calls {
	case 1:
		return &gatedDeltaStream{
			firstDelta: c.firstDelta,
			release:    c.release,
		}, nil
	case 2:
		if !containsAssistantMessage(prompt.History, "first answer") {
			c.t.Fatalf("follow-up prompt missing first assistant output: %#v", prompt.History)
		}
		userContexts := contextContents(prompt.History, "user")
		if len(userContexts) != 1 || !strings.Contains(userContexts[0], "<user_shell_command>") {
			c.t.Fatalf("follow-up prompt missing user shell command context: %#v", prompt.History)
		}
		item := dexco.AssistantMessageItem("continued after user shell")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", c.calls)
		return nil, nil
	}
}

// Codex lets a user-shell command complete while an assistant turn is active
// without replacing that turn. Dexco records the completed command as a pending
// contextual user item so it is replayed at the next safe continuation point.
func TestSessionUserShellCommandRecordDuringActiveTurnQueuesFollowUp(t *testing.T) {
	t.Parallel()

	client := &activeUserShellRecordClient{
		t:          t,
		firstDelta: make(chan struct{}),
		release:    make(chan struct{}),
	}
	session := newContextTestSession(t, client)
	resultCh := make(chan turnResult, 1)
	go func() {
		result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: "active turn"},
		}, dexco.NopSink{})
		resultCh <- turnResult{result: result, err: err}
	}()

	<-client.firstDelta
	if err := session.RecordUserShellCommand(dexco.UserShellCommandRecord{
		Command:  "printf user-shell",
		ExitCode: 0,
		Duration: 250 * time.Millisecond,
		Output:   "user-shell",
	}); err != nil {
		t.Fatalf("RecordUserShellCommand() error = %v", err)
	}
	close(client.release)

	outcome := <-resultCh
	if outcome.err != nil {
		t.Fatalf("SubmitUserInput() error = %v", outcome.err)
	}
	if outcome.result.ModelCalls != 2 {
		t.Fatalf("ModelCalls = %d, want 2", outcome.result.ModelCalls)
	}
	if outcome.result.FinalMessage != "continued after user shell" {
		t.Fatalf("FinalMessage = %q, want continued after user shell", outcome.result.FinalMessage)
	}
}

// Adapted from Codex subagent notification tests. Dexco does not own
// multi-agent execution, but embedders can record a completed/errored child
// status as the same contextual user fragment Codex delivers to the parent.
func TestSessionRecordsSubagentNotificationForNextTurn(t *testing.T) {
	t.Parallel()

	client := &additionalContextClient{t: t}
	session := newContextTestSession(t, client)
	if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "before child"},
	}, dexco.NopSink{}); err != nil {
		t.Fatalf("SubmitUserInput(first) error = %v", err)
	}
	if err := session.RecordSubagentNotification(dexco.SubagentNotificationRecord{
		AgentPath: "/root/worker",
		Status:    map[string]string{"completed": "child done"},
	}); err != nil {
		t.Fatalf("RecordSubagentNotification() error = %v", err)
	}
	if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "after child"},
	}, dexco.NopSink{}); err != nil {
		t.Fatalf("SubmitUserInput(second) error = %v", err)
	}

	wantRecord := `<subagent_notification>
{"agent_path":"/root/worker","status":{"completed":"child done"}}
</subagent_notification>`
	assertStringSliceEqual(t, itemLayout(client.prompts[1].History), []string{
		"user:before child",
		"assistant:context observed",
		"context:user:" + wantRecord,
		"user:after child",
	}, "second prompt history layout")
	if containsUserInput(client.prompts[1].History, wantRecord) {
		t.Fatalf("subagent notification leaked as user input: %#v", client.prompts[1].History)
	}
}

// Adapted from Codex session_prefix bounded completion-message coverage.
// Subagent status is model-visible parent context, so large errored/completed
// payloads must be bounded before they can consume future prompt budget.
func TestSessionSubagentNotificationStatusIsBounded(t *testing.T) {
	t.Parallel()

	client := &additionalContextClient{t: t}
	session := newContextTestSession(t, client)
	longStatus := "stream disconnected " + strings.Repeat("status-detail ", 1_000) + "tail-status"
	if err := session.RecordSubagentNotification(dexco.SubagentNotificationRecord{
		AgentPath: "/root/worker",
		Status: map[string]string{
			"errored": longStatus,
		},
	}); err != nil {
		t.Fatalf("RecordSubagentNotification() error = %v", err)
	}
	if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "inspect child status"},
	}, dexco.NopSink{}); err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	contexts := contextContents(client.prompts[0].History, "user")
	if len(contexts) != 1 {
		t.Fatalf("subagent contexts = %#v, want one", contexts)
	}
	got := contexts[0]
	if len([]rune(got)) > 4096 {
		t.Fatalf("subagent notification length = %d, want bounded below 4096", len([]rune(got)))
	}
	for _, want := range []string{
		`"agent_path":"/root/worker"`,
		`"truncated":true`,
		`"original_character_count"`,
		"subagent notification status characters truncated",
		"tail-status",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("bounded subagent notification missing %q: %q", want, got)
		}
	}
}

type activeSubagentNotificationClient struct {
	t          *testing.T
	firstDelta chan struct{}
	release    chan struct{}
	calls      int
}

func (c *activeSubagentNotificationClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	switch c.calls {
	case 1:
		return &gatedDeltaStream{
			firstDelta: c.firstDelta,
			release:    c.release,
		}, nil
	case 2:
		if !containsAssistantMessage(prompt.History, "first answer") {
			c.t.Fatalf("follow-up prompt missing first assistant output: %#v", prompt.History)
		}
		userContexts := contextContents(prompt.History, "user")
		if len(userContexts) != 1 || !strings.Contains(userContexts[0], "<subagent_notification>") {
			c.t.Fatalf("follow-up prompt missing subagent notification context: %#v", prompt.History)
		}
		item := dexco.AssistantMessageItem("continued after subagent notification")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", c.calls)
		return nil, nil
	}
}

// Codex can deliver a subagent notification while the parent assistant turn is
// still active; the parent sees it at the next model continuation boundary.
func TestSessionSubagentNotificationDuringActiveTurnQueuesFollowUp(t *testing.T) {
	t.Parallel()

	client := &activeSubagentNotificationClient{
		t:          t,
		firstDelta: make(chan struct{}),
		release:    make(chan struct{}),
	}
	session := newContextTestSession(t, client)
	resultCh := make(chan turnResult, 1)
	go func() {
		result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: "active parent turn"},
		}, dexco.NopSink{})
		resultCh <- turnResult{result: result, err: err}
	}()

	<-client.firstDelta
	if err := session.RecordSubagentNotification(dexco.SubagentNotificationRecord{
		AgentPath: "/root/worker",
		Status:    "running",
	}); err != nil {
		t.Fatalf("RecordSubagentNotification() error = %v", err)
	}
	close(client.release)

	outcome := <-resultCh
	if outcome.err != nil {
		t.Fatalf("SubmitUserInput() error = %v", outcome.err)
	}
	if outcome.result.ModelCalls != 2 {
		t.Fatalf("ModelCalls = %d, want 2", outcome.result.ModelCalls)
	}
	if outcome.result.FinalMessage != "continued after subagent notification" {
		t.Fatalf("FinalMessage = %q, want continued after subagent notification", outcome.result.FinalMessage)
	}
}

// Adapted from Codex AGENTS.md tests. Dexco does not discover files itself, but
// it accepts an already-loaded snapshot and renders it as the same user-role
// contextual fragment Codex uses for AGENTS.md instructions.
func TestSessionContextInstructionsSnapshotPersistsAndReportsSources(t *testing.T) {
	t.Parallel()

	sources := []dexco.InstructionSource{
		{URI: "file:///home/user/.codex/AGENTS.md", Label: "global"},
		{URI: "file:///work/repo/AGENTS.md", Label: "project"},
	}
	snapshot := dexco.ContextInstructionsSnapshot{
		Text:    "Prefer gofmt.\n\nProject tests use go test ./...",
		Scope:   "/work/repo",
		Sources: sources,
	}
	client := &additionalContextClient{t: t}
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{
		ContextInstructions: dexco.ContextInstructionsConfig{
			Snapshot: &snapshot,
		},
	}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	if got := session.ContextInstructionSources(); !reflect.DeepEqual(got, sources) {
		t.Fatalf("ContextInstructionSources() = %#v, want %#v", got, sources)
	}
	submitContextTurn(t, session, "first turn", nil)
	submitContextTurn(t, session, "second turn", nil)

	wantFragment := "# AGENTS.md instructions for /work/repo\n\n<INSTRUCTIONS>\nPrefer gofmt.\n\nProject tests use go test ./...\n</INSTRUCTIONS>"
	assertStringSliceEqual(t, contextContents(client.prompts[0].History, "user"), []string{
		wantFragment,
	}, "first prompt context instructions")
	assertStringSliceEqual(t, contextContents(client.prompts[1].History, "user"), []string{
		wantFragment,
	}, "second prompt context instructions")
	assertStringSliceEqual(t, userInputContents(client.prompts[1].History), []string{
		"first turn",
		"second turn",
	}, "ordinary turns do not rewrite instruction history")
}

func TestSessionContextInstructionsAppendReplacementAndRemoval(t *testing.T) {
	t.Parallel()

	client := &additionalContextClient{t: t}
	session := newContextTestSession(t, client)
	first := dexco.ContextInstructionsSnapshot{
		Text:    "first instructions",
		Scope:   "/work/repo",
		Sources: []dexco.InstructionSource{{URI: "file:///one", Label: "one"}},
	}
	if err := session.SetContextInstructions(first); err != nil {
		t.Fatalf("SetContextInstructions(first) error = %v", err)
	}
	submitContextTurn(t, session, "first turn", nil)

	sameModelVisibleText := dexco.ContextInstructionsSnapshot{
		Text:    first.Text,
		Scope:   first.Scope,
		Sources: []dexco.InstructionSource{{URI: "file:///two", Label: "two"}},
	}
	if err := session.SetContextInstructions(sameModelVisibleText); err != nil {
		t.Fatalf("SetContextInstructions(same) error = %v", err)
	}
	if got := session.ContextInstructionSources(); !reflect.DeepEqual(got, sameModelVisibleText.Sources) {
		t.Fatalf("ContextInstructionSources() = %#v, want updated source-only attribution", got)
	}
	submitContextTurn(t, session, "second turn", nil)

	second := dexco.ContextInstructionsSnapshot{
		Text:  "second instructions",
		Scope: "/work/repo",
	}
	if err := session.SetContextInstructions(second); err != nil {
		t.Fatalf("SetContextInstructions(second) error = %v", err)
	}
	submitContextTurn(t, session, "third turn", nil)
	if err := session.ClearContextInstructions(); err != nil {
		t.Fatalf("ClearContextInstructions() error = %v", err)
	}
	submitContextTurn(t, session, "fourth turn", nil)

	initial := "# AGENTS.md instructions for /work/repo\n\n<INSTRUCTIONS>\nfirst instructions\n</INSTRUCTIONS>"
	replacement := "# AGENTS.md instructions for /work/repo\n\n<INSTRUCTIONS>\nThese AGENTS.md instructions replace all previously provided AGENTS.md instructions.\n\nsecond instructions\n</INSTRUCTIONS>"
	removal := "# AGENTS.md instructions\n\n<INSTRUCTIONS>\nThe previously provided AGENTS.md instructions no longer apply.\n</INSTRUCTIONS>"
	assertStringSliceEqual(t, contextContents(client.prompts[1].History, "user"), []string{
		initial,
	}, "source-only update does not emit model-visible diff")
	assertStringSliceEqual(t, contextContents(client.prompts[2].History, "user"), []string{
		initial,
		replacement,
	}, "replacement context instructions")
	assertStringSliceEqual(t, contextContents(client.prompts[3].History, "user"), []string{
		initial,
		replacement,
		removal,
	}, "removal context instructions")
	if got := session.ContextInstructionSources(); got != nil {
		t.Fatalf("ContextInstructionSources() after clear = %#v, want nil", got)
	}
}

func TestSessionContextInstructionsAreBounded(t *testing.T) {
	t.Parallel()

	client := &additionalContextClient{t: t}
	longText := "agents-head-" + strings.Repeat("a", 500) + "-agents-tail"
	snapshot := dexco.ContextInstructionsSnapshot{
		Text:  longText,
		Scope: "/work/repo",
	}
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{
		ContextInstructions: dexco.ContextInstructionsConfig{
			Snapshot: &snapshot,
			MaxChars: 80,
		},
	}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	submitContextTurn(t, session, "bounded instructions", nil)

	contexts := contextContents(client.prompts[0].History, "user")
	if len(contexts) != 1 {
		t.Fatalf("context instructions = %#v, want one", contexts)
	}
	if !strings.Contains(contexts[0], "agents-head-") ||
		!strings.Contains(contexts[0], "-agents-tail") ||
		!strings.Contains(contexts[0], "AGENTS.md instruction characters truncated") {
		t.Fatalf("bounded context instructions missing truncation details: %q", contexts[0])
	}
	if len([]rune(contexts[0])) > 260 {
		t.Fatalf("context instructions length = %d, want bounded", len([]rune(contexts[0])))
	}
}

// Adapted from Codex environment_context render tests. Dexco does not discover
// environments itself, but callers can provide a snapshot that is rendered as
// the same user-role contextual fragment Codex injects into model history.
func TestSessionEnvironmentContextSnapshotRendersPortableCodexShape(t *testing.T) {
	t.Parallel()

	snapshot := dexco.EnvironmentContextSnapshot{
		Environments: []dexco.EnvironmentState{{
			ID:    "local",
			CWD:   "/repo",
			Shell: "bash",
		}},
		CurrentDate: "2026-02-26",
		Timezone:    "America/Los_Angeles",
	}
	client := &additionalContextClient{t: t}
	session := newEnvironmentContextSession(t, client, dexco.EnvironmentContextConfig{
		Snapshot: &snapshot,
	})

	submitContextTurn(t, session, "first turn", nil)
	submitContextTurn(t, session, "second turn", nil)

	want := "<environment_context>\n  <cwd>/repo</cwd>\n  <shell>bash</shell>\n  <current_date>2026-02-26</current_date>\n  <timezone>America/Los_Angeles</timezone>\n</environment_context>"
	assertStringSliceEqual(t, contextContents(client.prompts[0].History, "user"), []string{
		want,
	}, "first prompt environment context")
	assertStringSliceEqual(t, contextContents(client.prompts[1].History, "user"), []string{
		want,
	}, "ordinary turns retain environment context")
}

// Adapted from Codex's single_environment_diff_ignores_unknown_shell test.
// Dexco does not discover shells itself, but embedders may hydrate the shell
// after an initial snapshot. Suppressing only that legacy single-environment
// transition avoids cache-churning context duplicates while still allowing a
// later real shell change to become model-visible.
func TestSessionEnvironmentContextSingleEnvironmentIgnoresShellBecomingKnown(t *testing.T) {
	t.Parallel()

	client := &additionalContextClient{t: t}
	session := newContextTestSession(t, client)
	unknownShell := dexco.EnvironmentContextSnapshot{
		Environments: []dexco.EnvironmentState{{
			ID:  "local",
			CWD: "/repo",
		}},
	}
	discoveredShell := dexco.EnvironmentContextSnapshot{
		Environments: []dexco.EnvironmentState{{
			ID:    "local",
			CWD:   "/repo",
			Shell: "zsh",
		}},
	}
	changedShell := dexco.EnvironmentContextSnapshot{
		Environments: []dexco.EnvironmentState{{
			ID:    "local",
			CWD:   "/repo",
			Shell: "bash",
		}},
	}

	if err := session.SetEnvironmentContext(unknownShell); err != nil {
		t.Fatalf("SetEnvironmentContext(unknownShell) error = %v", err)
	}
	submitContextTurn(t, session, "unknown shell", nil)
	if err := session.SetEnvironmentContext(discoveredShell); err != nil {
		t.Fatalf("SetEnvironmentContext(discoveredShell) error = %v", err)
	}
	submitContextTurn(t, session, "discovered shell", nil)
	if err := session.SetEnvironmentContext(changedShell); err != nil {
		t.Fatalf("SetEnvironmentContext(changedShell) error = %v", err)
	}
	submitContextTurn(t, session, "changed shell", nil)

	unknownRendered := "<environment_context>\n  <cwd>/repo</cwd>\n</environment_context>"
	changedRendered := "<environment_context>\n  <cwd>/repo</cwd>\n  <shell>bash</shell>\n</environment_context>"
	assertStringSliceEqual(t, contextContents(client.prompts[1].History, "user"), []string{
		unknownRendered,
	}, "shell discovery does not append a duplicate environment context")
	assertStringSliceEqual(t, contextContents(client.prompts[2].History, "user"), []string{
		unknownRendered,
		changedRendered,
	}, "later real shell changes still append environment context")
}

func TestSessionEnvironmentContextDateTimezoneOnly(t *testing.T) {
	t.Parallel()

	snapshot := dexco.EnvironmentContextSnapshot{
		CurrentDate: "2026-02-26",
		Timezone:    "America/Los_Angeles",
	}
	client := &additionalContextClient{t: t}
	session := newEnvironmentContextSession(t, client, dexco.EnvironmentContextConfig{
		Snapshot: &snapshot,
	})

	submitContextTurn(t, session, "read-only environment", nil)

	want := "<environment_context>\n  <current_date>2026-02-26</current_date>\n  <timezone>America/Los_Angeles</timezone>\n</environment_context>"
	assertStringSliceEqual(t, contextContents(client.prompts[0].History, "user"), []string{
		want,
	}, "date/timezone-only environment context")
}

func TestSessionEnvironmentContextUpdatesAndSortsMultipleEnvironments(t *testing.T) {
	t.Parallel()

	client := &additionalContextClient{t: t}
	session := newContextTestSession(t, client)
	first := dexco.EnvironmentContextSnapshot{
		Environments: []dexco.EnvironmentState{
			{ID: "remote", CWD: "/repo/remote", Shell: "powershell"},
			{ID: "local", CWD: "/repo/local", Shell: "bash"},
		},
		CurrentDate: "2026-02-26",
	}
	if err := session.SetEnvironmentContext(first); err != nil {
		t.Fatalf("SetEnvironmentContext(first) error = %v", err)
	}
	submitContextTurn(t, session, "first environment turn", nil)
	if err := session.SetEnvironmentContext(first); err != nil {
		t.Fatalf("SetEnvironmentContext(same) error = %v", err)
	}
	submitContextTurn(t, session, "same environment turn", nil)

	second := dexco.EnvironmentContextSnapshot{
		Environments: []dexco.EnvironmentState{
			{ID: "remote", CWD: "/repo/remote", Shell: "powershell"},
			{ID: "local", CWD: "/repo/local", Shell: "bash"},
		},
		CurrentDate: "2026-02-26",
		Subagents:   "- agent-1: atlas\n- agent-2",
	}
	if err := session.SetEnvironmentContext(second); err != nil {
		t.Fatalf("SetEnvironmentContext(second) error = %v", err)
	}
	submitContextTurn(t, session, "subagents environment turn", nil)

	firstRendered := "<environment_context>\n  <environments>\n    <environment id=\"local\">\n      <cwd>/repo/local</cwd>\n      <shell>bash</shell>\n    </environment>\n    <environment id=\"remote\">\n      <cwd>/repo/remote</cwd>\n      <shell>powershell</shell>\n    </environment>\n  </environments>\n  <current_date>2026-02-26</current_date>\n</environment_context>"
	secondRendered := "<environment_context>\n  <environments>\n    <environment id=\"local\">\n      <cwd>/repo/local</cwd>\n      <shell>bash</shell>\n    </environment>\n    <environment id=\"remote\">\n      <cwd>/repo/remote</cwd>\n      <shell>powershell</shell>\n    </environment>\n  </environments>\n  <current_date>2026-02-26</current_date>\n  <subagents>\n    - agent-1: atlas\n    - agent-2\n  </subagents>\n</environment_context>"
	assertStringSliceEqual(t, contextContents(client.prompts[1].History, "user"), []string{
		firstRendered,
	}, "unchanged environment context does not duplicate")
	assertStringSliceEqual(t, contextContents(client.prompts[2].History, "user"), []string{
		firstRendered,
		secondRendered,
	}, "changed environment context appends")
}

func TestSessionEnvironmentContextRendersEnvironmentStatuses(t *testing.T) {
	t.Parallel()

	snapshot := dexco.EnvironmentContextSnapshot{
		Environments: []dexco.EnvironmentState{
			{ID: "old", Status: "unavailable"},
			{ID: "remote", CWD: "/repo/remote", Status: "starting"},
		},
	}
	client := &additionalContextClient{t: t}
	session := newEnvironmentContextSession(t, client, dexco.EnvironmentContextConfig{
		Snapshot: &snapshot,
	})

	submitContextTurn(t, session, "environment statuses", nil)

	want := "<environment_context>\n  <environments>\n    <environment id=\"old\" status=\"unavailable\" />\n    <environment id=\"remote\">\n      <cwd>/repo/remote</cwd>\n      <status>starting</status>\n    </environment>\n  </environments>\n</environment_context>"
	assertStringSliceEqual(t, contextContents(client.prompts[0].History, "user"), []string{
		want,
	}, "environment status context")
}

func TestSessionEnvironmentContextSingleStartingUsesEnvironmentWrapper(t *testing.T) {
	t.Parallel()

	snapshot := dexco.EnvironmentContextSnapshot{
		Environments: []dexco.EnvironmentState{
			{ID: "remote", CWD: "/repo/remote", Status: "starting"},
		},
	}
	client := &additionalContextClient{t: t}
	session := newEnvironmentContextSession(t, client, dexco.EnvironmentContextConfig{
		Snapshot: &snapshot,
	})

	submitContextTurn(t, session, "single starting environment", nil)

	want := "<environment_context>\n  <environments>\n    <environment id=\"remote\">\n      <cwd>/repo/remote</cwd>\n      <status>starting</status>\n    </environment>\n  </environments>\n</environment_context>"
	assertStringSliceEqual(t, contextContents(client.prompts[0].History, "user"), []string{
		want,
	}, "single status-bearing environment context")
}

func newEnvironmentContextSession(
	t *testing.T,
	client dexco.ModelClient,
	config dexco.EnvironmentContextConfig,
) *dexco.Session {
	t.Helper()
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{
		EnvironmentContext: config,
	}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	return session
}

type additionalContextClient struct {
	t       *testing.T
	prompts []dexco.Prompt
}

func (c *additionalContextClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	message := dexco.AssistantMessageItem("context observed")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &message},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

// Adapted from Codex core's additional_context tests. Dexco exposes the
// provider-agnostic shape as context history items: visible to the model, but
// not collapsed into the user's chat message item.
func TestSessionAdditionalContextIsModelVisibleButNotUserHistory(t *testing.T) {
	t.Parallel()

	client := &additionalContextClient{t: t}
	session := newContextTestSession(t, client)
	_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "inspect context"},
		AdditionalContext: map[string]dexco.AdditionalContextEntry{
			"browser_info": {
				Value: "tab one",
				Kind:  dexco.AdditionalContextUntrusted,
			},
			"automation_info": {
				Value: "run one",
				Kind:  dexco.AdditionalContextApplication,
			},
		},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	prompt := client.prompts[0]
	assertStringSliceEqual(t, contextContents(prompt.History, "developer"), []string{
		"<automation_info>run one</automation_info>",
	}, "developer context")
	assertStringSliceEqual(t, contextContents(prompt.History, "user"), []string{
		"<external_browser_info>tab one</external_browser_info>",
	}, "user context")
	if containsUserInput(prompt.History, "<external_browser_info>tab one</external_browser_info>") {
		t.Fatalf("additional context leaked as user input: %#v", prompt.History)
	}
	if !containsUserInput(prompt.History, "inspect context") {
		t.Fatalf("prompt missing actual user input: %#v", prompt.History)
	}
}

// Codex retains additional context in model context and deduplicates unchanged
// active entries. Re-adding a previously removed value should make it visible
// again at the new turn boundary.
func TestSessionAdditionalContextDedupesWhileRetainingModelContext(t *testing.T) {
	t.Parallel()

	client := &additionalContextClient{t: t}
	session := newContextTestSession(t, client)
	submitContextTurn(t, session, "first turn", map[string]dexco.AdditionalContextEntry{
		"automation_info": {
			Value: "run one",
			Kind:  dexco.AdditionalContextUntrusted,
		},
		"browser_info": {
			Value: "tab one",
			Kind:  dexco.AdditionalContextUntrusted,
		},
	})
	submitContextTurn(t, session, "second turn", map[string]dexco.AdditionalContextEntry{
		"automation_info": {
			Value: "run one",
			Kind:  dexco.AdditionalContextUntrusted,
		},
		"terminal_info": {
			Value: "pty one",
			Kind:  dexco.AdditionalContextUntrusted,
		},
	})
	submitContextTurn(t, session, "third turn", map[string]dexco.AdditionalContextEntry{
		"automation_info": {
			Value: "run one",
			Kind:  dexco.AdditionalContextUntrusted,
		},
		"browser_info": {
			Value: "tab one",
			Kind:  dexco.AdditionalContextUntrusted,
		},
		"terminal_info": {
			Value: "pty one",
			Kind:  dexco.AdditionalContextUntrusted,
		},
	})

	assertStringSliceEqual(t, allContextContents(client.prompts[0].History), []string{
		"<external_automation_info>run one</external_automation_info>",
		"<external_browser_info>tab one</external_browser_info>",
	}, "first prompt context")
	assertStringSliceEqual(t, allContextContents(client.prompts[1].History), []string{
		"<external_automation_info>run one</external_automation_info>",
		"<external_browser_info>tab one</external_browser_info>",
		"<external_terminal_info>pty one</external_terminal_info>",
	}, "second prompt context")
	assertStringSliceEqual(t, allContextContents(client.prompts[2].History), []string{
		"<external_automation_info>run one</external_automation_info>",
		"<external_browser_info>tab one</external_browser_info>",
		"<external_terminal_info>pty one</external_terminal_info>",
		"<external_browser_info>tab one</external_browser_info>",
	}, "third prompt context")
}

// Codex caps large additional context before model input. Dexco preserves the
// bounded-context invariant so applications cannot inject unbounded prompt text
// through this side channel.
func TestSessionAdditionalContextValuesAreTruncated(t *testing.T) {
	t.Parallel()

	client := &additionalContextClient{t: t}
	session := newContextTestSession(t, client)
	longBrowserValue := "browser-head-" + strings.Repeat("b", 40_000) + "browser-tail"
	longAutomationValue := "automation-head-" + strings.Repeat("a", 40_000) + "automation-tail"
	submitContextTurn(t, session, "summarize context", map[string]dexco.AdditionalContextEntry{
		"automation_info": {
			Value: longAutomationValue,
			Kind:  dexco.AdditionalContextApplication,
		},
		"browser_info": {
			Value: longBrowserValue,
			Kind:  dexco.AdditionalContextUntrusted,
		},
	})

	developerContexts := contextContents(client.prompts[0].History, "developer")
	if len(developerContexts) != 1 {
		t.Fatalf("developer context = %#v, want one item", developerContexts)
	}
	assertTruncatedContext(t, developerContexts[0], "<automation_info>automation-head-", "automation-tail</automation_info>")
	userContexts := contextContents(client.prompts[0].History, "user")
	if len(userContexts) != 1 {
		t.Fatalf("user context = %#v, want one item", userContexts)
	}
	assertTruncatedContext(t, userContexts[0], "<external_browser_info>browser-head-", "browser-tail</external_browser_info>")
}

type rolloutBudgetClient struct {
	t       *testing.T
	prompts []dexco.Prompt
	usages  []dexco.TokenUsage
}

func (c *rolloutBudgetClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	message := dexco.AssistantMessageItem("budget observed")
	completed := dexco.ResponseEvent{Type: dexco.EventCompleted, EndTurn: boolPtr(true)}
	if index := len(c.prompts) - 1; index < len(c.usages) {
		usage := c.usages[index]
		completed.TokenUsage = &usage
	}
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &message},
			completed,
		},
	}, nil
}

func rolloutBudgetMessage(remainingTokens int64) string {
	return fmt.Sprintf(
		"<rollout_budget>\nYou have %d weighted tokens left in the shared session token budget.\n</rollout_budget>",
		remainingTokens,
	)
}

// Adapted from Codex rollout_budget tests. Dexco does not own a thread tree or
// compaction pipeline, but it preserves the portable loop contract: budget
// reminders are developer context, initial budget is stated before sampling,
// and crossed thresholds are restated after weighted completed-response usage.
func TestSessionRolloutBudgetAddsInitialAndThresholdReminders(t *testing.T) {
	t.Parallel()

	client := &rolloutBudgetClient{
		t: t,
		usages: []dexco.TokenUsage{{
			InputTokens:       60,
			CachedInputTokens: 40,
			OutputTokens:      15,
		}},
	}
	session := newRolloutBudgetSession(t, client, dexco.RolloutBudgetConfig{
		LimitTokens:               100,
		ReminderAtRemainingTokens: []int64{75, 50, 25},
		SamplingTokenWeight:       2,
		PrefillTokenWeight:        0.5,
	})

	for _, input := range []string{"first budget turn", "second budget turn"} {
		if _, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: input},
		}, dexco.NopSink{}); err != nil {
			t.Fatalf("SubmitUserInput(%q) error = %v", input, err)
		}
	}

	if len(client.prompts) != 2 {
		t.Fatalf("model prompts = %d, want 2", len(client.prompts))
	}
	assertStringSliceEqual(t, contextContents(client.prompts[0].History, "developer"), []string{
		rolloutBudgetMessage(100),
	}, "first prompt rollout budget")
	assertStringSliceEqual(t, contextContents(client.prompts[1].History, "developer"), []string{
		rolloutBudgetMessage(100),
		rolloutBudgetMessage(60),
	}, "second prompt rollout budget")
}

// Codex reports session-budget exhaustion after completed usage and rejects
// later turns without retrying/sampling. Dexco exposes the same library-level
// decision as ErrRolloutBudgetExceeded while leaving UI error events to callers.
func TestSessionRolloutBudgetExhaustionFailsCurrentAndLaterTurns(t *testing.T) {
	t.Parallel()

	client := &rolloutBudgetClient{
		t: t,
		usages: []dexco.TokenUsage{{
			OutputTokens: 30,
		}},
	}
	session := newRolloutBudgetSession(t, client, dexco.RolloutBudgetConfig{
		LimitTokens:               30,
		ReminderAtRemainingTokens: []int64{20, 10},
	})

	_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "exhaust budget"},
	}, dexco.NopSink{})
	if !errors.Is(err, dexco.ErrRolloutBudgetExceeded) {
		t.Fatalf("first SubmitUserInput() error = %v, want ErrRolloutBudgetExceeded", err)
	}
	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "try after exhausted"},
	}, dexco.NopSink{})
	if !errors.Is(err, dexco.ErrRolloutBudgetExceeded) {
		t.Fatalf("second SubmitUserInput() error = %v, want ErrRolloutBudgetExceeded", err)
	}
	if len(client.prompts) != 1 {
		t.Fatalf("model prompts = %d, want only first turn sampled", len(client.prompts))
	}
	assertStringSliceEqual(t, contextContents(client.prompts[0].History, "developer"), []string{
		rolloutBudgetMessage(30),
	}, "first prompt rollout budget")
}

func newRolloutBudgetSession(
	t *testing.T,
	client dexco.ModelClient,
	budget dexco.RolloutBudgetConfig,
) *dexco.Session {
	t.Helper()
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{RolloutBudget: budget}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	return session
}

type steerFollowUpClient struct {
	t          *testing.T
	firstDelta chan struct{}
	release    chan struct{}
	prompts    []dexco.Prompt
	calls      int
}

func (c *steerFollowUpClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	c.calls++
	switch c.calls {
	case 1:
		return &gatedDeltaStream{
			firstDelta: c.firstDelta,
			release:    c.release,
		}, nil
	case 2:
		assertStringSliceEqual(c.t, userInputContents(prompt.History), []string{
			"first prompt",
			"second prompt",
		}, "follow-up prompt user inputs")
		if !containsAssistantMessage(prompt.History, "first answer") {
			c.t.Fatalf("follow-up prompt missing first assistant output: %#v", prompt.History)
		}
		item := dexco.AssistantMessageItem("continued after steer")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", c.calls)
		return nil, nil
	}
}

type gatedDeltaStream struct {
	firstDelta chan struct{}
	release    chan struct{}
	stage      int
}

func (s *gatedDeltaStream) Recv() (dexco.ResponseEvent, error) {
	switch s.stage {
	case 0:
		s.stage++
		close(s.firstDelta)
		return dexco.ResponseEvent{Type: dexco.EventOutputTextDelta, Delta: "first answer"}, nil
	case 1:
		s.stage++
		<-s.release
		return dexco.ResponseEvent{Type: dexco.EventCompleted, EndTurn: boolPtr(true)}, nil
	default:
		return dexco.ResponseEvent{}, io.EOF
	}
}

type reasoningSteerClient struct {
	t               *testing.T
	reasoningSeen   chan struct{}
	release         chan struct{}
	prompts         []dexco.Prompt
	reasoningStream *gatedReasoningStream
}

func (c *reasoningSteerClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	switch len(c.prompts) {
	case 1:
		c.reasoningStream = &gatedReasoningStream{
			reasoningSeen: c.reasoningSeen,
			release:       c.release,
		}
		return c.reasoningStream, nil
	case 2:
		assertStringSliceEqual(c.t, userInputContents(prompt.History), []string{
			"first prompt",
			"second prompt",
		}, "follow-up prompt user inputs")
		if !containsReasoning(prompt.History, "thinking") {
			c.t.Fatalf("follow-up prompt missing preserved reasoning item: %#v", prompt.History)
		}
		if !containsToolCall(prompt.History, "reasoning-call") {
			c.t.Fatalf("follow-up prompt missing preserved tool call: %#v", prompt.History)
		}
		if !containsToolResult(prompt.History, "reasoning-call", "preserved tool output") {
			c.t.Fatalf("follow-up prompt missing preserved tool result: %#v", prompt.History)
		}
		if !containsAssistantMessage(prompt.History, "first answer") {
			c.t.Fatalf("follow-up prompt missing preserved assistant output: %#v", prompt.History)
		}
		item := dexco.AssistantMessageItem("continued after reasoning steer")
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

type gatedReasoningStream struct {
	reasoningSeen chan struct{}
	release       chan struct{}
	stage         int
}

func (s *gatedReasoningStream) Recv() (dexco.ResponseEvent, error) {
	switch s.stage {
	case 0:
		s.stage++
		close(s.reasoningSeen)
		return dexco.ResponseEvent{Type: dexco.EventReasoningDelta, Delta: "thinking"}, nil
	case 1:
		s.stage++
		<-s.release
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "reasoning-call",
			Name:      "echo_tool",
			Arguments: json.RawMessage(`{"value":"preserved tool output"}`),
		})
		return dexco.ResponseEvent{Type: dexco.EventOutputItemDone, Item: &item}, nil
	case 2:
		s.stage++
		return dexco.ResponseEvent{Type: dexco.EventOutputTextDelta, Delta: "first answer"}, nil
	case 3:
		s.stage++
		item := dexco.AssistantMessageItem("first answer")
		return dexco.ResponseEvent{Type: dexco.EventOutputItemDone, Item: &item}, nil
	case 4:
		s.stage++
		return dexco.ResponseEvent{Type: dexco.EventCompleted, EndTurn: boolPtr(true)}, nil
	default:
		return dexco.ResponseEvent{}, io.EOF
	}
}

type turnIDCaptureSink struct {
	dexco.NopSink
	started chan string
}

func (s *turnIDCaptureSink) OnTurnStarted(_ context.Context, turn dexco.Turn) error {
	s.started <- turn.ID
	return nil
}

// Adapted from Codex pending_input behavior. Dexco does not yet implement
// Codex's full turn scheduler, but it preserves the portable library invariant:
// steering received while a turn is active is queued and sent in the next
// follow-up request instead of becoming an unsafe concurrent turn.
func TestSessionSteeredInputTriggersFollowUpRequest(t *testing.T) {
	t.Parallel()

	client := &steerFollowUpClient{
		t:          t,
		firstDelta: make(chan struct{}),
		release:    make(chan struct{}),
	}
	session := newContextTestSession(t, client)
	resultCh := make(chan turnResult, 1)
	go func() {
		result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: "first prompt"},
		}, dexco.NopSink{})
		resultCh <- turnResult{result: result, err: err}
	}()

	<-client.firstDelta
	if err := session.SteerUserInput(context.Background(), dexco.UserInput{Content: "second prompt"}); err != nil {
		t.Fatalf("SteerUserInput() error = %v", err)
	}
	close(client.release)

	outcome := <-resultCh
	if outcome.err != nil {
		t.Fatalf("SubmitUserInput() error = %v", outcome.err)
	}
	if outcome.result.ModelCalls != 2 {
		t.Fatalf("ModelCalls = %d, want 2", outcome.result.ModelCalls)
	}
	if outcome.result.FinalMessage != "continued after steer" {
		t.Fatalf("FinalMessage = %q, want continued after steer", outcome.result.FinalMessage)
	}
}

// Adapted from Codex pending_input
// `user_input_does_not_preempt_after_reasoning_item`. Once a reasoning item has
// started, new user steering must not replace the still-active turn. Dexco keeps
// that nuance by draining pending input only after the current sampling request
// and any completed tool work are preserved for the follow-up request.
func TestSessionSteeredInputDoesNotPreemptAfterReasoning(t *testing.T) {
	t.Parallel()

	client := &reasoningSteerClient{
		t:             t,
		reasoningSeen: make(chan struct{}),
		release:       make(chan struct{}),
	}
	router, err := dexco.NewRouter(echoTool{})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	resultCh := make(chan turnResult, 1)
	go func() {
		result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: "first prompt"},
		}, dexco.NopSink{})
		resultCh <- turnResult{result: result, err: err}
	}()

	<-client.reasoningSeen
	if err := session.SteerUserInput(context.Background(), dexco.UserInput{Content: "second prompt"}); err != nil {
		close(client.release)
		t.Fatalf("SteerUserInput() error = %v", err)
	}
	close(client.release)

	outcome := <-resultCh
	if outcome.err != nil {
		t.Fatalf("SubmitUserInput() error = %v", outcome.err)
	}
	if outcome.result.ModelCalls != 2 {
		t.Fatalf("ModelCalls = %d, want 2", outcome.result.ModelCalls)
	}
	if outcome.result.FinalMessage != "continued after reasoning steer" {
		t.Fatalf("FinalMessage = %q, want continued after reasoning steer", outcome.result.FinalMessage)
	}
}

// Adapted from Codex session `steer_input_enforces_expected_turn_id` and
// `steer_input_returns_active_turn_id`. A library client may send steering from
// stale UI state; Dexco must reject that input instead of appending it to
// whichever turn is currently active, and accepted steering reports the active
// turn ID so callers can keep correlating future updates.
func TestSessionSteeredInputEnforcesExpectedTurnIDAndReturnsActiveTurnID(t *testing.T) {
	t.Parallel()

	client := &steerFollowUpClient{
		t:          t,
		firstDelta: make(chan struct{}),
		release:    make(chan struct{}),
	}
	session := newContextTestSession(t, client)
	sink := &turnIDCaptureSink{started: make(chan string, 1)}
	resultCh := make(chan turnResult, 1)
	go func() {
		result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: "first prompt"},
		}, sink)
		resultCh <- turnResult{result: result, err: err}
	}()

	var activeTurnID string
	select {
	case activeTurnID = <-sink.started:
	case <-time.After(time.Second):
		close(client.release)
		t.Fatalf("SubmitUserInput did not emit turn started")
	}
	<-client.firstDelta

	_, err := session.SteerUserInputWithOptions(
		context.Background(),
		dexco.UserInput{Content: "stale prompt"},
		dexco.SteerUserInputOptions{ExpectedTurnID: "stale-turn"},
	)
	if err == nil || !strings.Contains(err.Error(), `expected active turn "stale-turn"`) {
		close(client.release)
		t.Fatalf("SteerUserInputWithOptions(stale) error = %v, want expected turn mismatch", err)
	}

	gotTurnID, err := session.SteerUserInputWithOptions(
		context.Background(),
		dexco.UserInput{Content: "second prompt"},
		dexco.SteerUserInputOptions{ExpectedTurnID: activeTurnID},
	)
	if err != nil {
		close(client.release)
		t.Fatalf("SteerUserInputWithOptions(active) error = %v", err)
	}
	if gotTurnID != activeTurnID {
		close(client.release)
		t.Fatalf("SteerUserInputWithOptions() turnID = %q, want %q", gotTurnID, activeTurnID)
	}
	close(client.release)

	outcome := <-resultCh
	if outcome.err != nil {
		t.Fatalf("SubmitUserInput() error = %v", outcome.err)
	}
	if outcome.result.ModelCalls != 2 {
		t.Fatalf("ModelCalls = %d, want 2", outcome.result.ModelCalls)
	}
}

// Codex queues pending input for the active turn; it does not let two user turns
// mutate the same session history concurrently. Dexco exposes the same
// distinction through SubmitUserInput vs SteerUserInput.
func TestSessionRejectsConcurrentSubmitWhileTurnActive(t *testing.T) {
	t.Parallel()

	client := &steerFollowUpClient{
		t:          t,
		firstDelta: make(chan struct{}),
		release:    make(chan struct{}),
	}
	session := newContextTestSession(t, client)
	resultCh := make(chan turnResult, 1)
	go func() {
		result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: "first prompt"},
		}, dexco.NopSink{})
		resultCh <- turnResult{result: result, err: err}
	}()

	<-client.firstDelta
	_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "unsafe concurrent prompt"},
	}, dexco.NopSink{})
	if err == nil || !strings.Contains(err.Error(), "turn already running") {
		t.Fatalf("concurrent SubmitUserInput() error = %v, want turn already running", err)
	}
	if err := session.SteerUserInput(context.Background(), dexco.UserInput{Content: "second prompt"}); err != nil {
		t.Fatalf("SteerUserInput() error = %v", err)
	}
	close(client.release)
	if outcome := <-resultCh; outcome.err != nil {
		t.Fatalf("SubmitUserInput() error = %v", outcome.err)
	}
}

type pendingInterruptClient struct {
	t     *testing.T
	calls int
}

func (c *pendingInterruptClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	switch c.calls {
	case 1:
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "wait-call",
			Name:      "interruptible_wait",
			Arguments: json.RawMessage(`{}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 2:
		assertStringSliceEqual(c.t, userInputContents(prompt.History), []string{
			"wait for input",
			"stop waiting",
		}, "follow-up prompt user inputs")
		if !containsToolResult(prompt.History, "wait-call", "Wait interrupted by new input.") {
			c.t.Fatalf("follow-up prompt missing interrupted wait output: %#v", prompt.History)
		}
		item := dexco.AssistantMessageItem("continued after interrupt")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", c.calls)
		return nil, nil
	}
}

type interruptibleWaitTool struct {
	started     chan struct{}
	interrupted chan struct{}
	release     <-chan struct{}
}

func (interruptibleWaitTool) Name() string {
	return "interruptible_wait"
}

func (interruptibleWaitTool) Spec() dexco.ToolSpec {
	return dexco.ToolSpec{Name: "interruptible_wait"}
}

func (interruptibleWaitTool) InterruptsOnPendingInput() bool {
	return true
}

func (t interruptibleWaitTool) Call(ctx context.Context, _ dexco.ToolCall) (dexco.ToolResult, error) {
	close(t.started)
	select {
	case <-ctx.Done():
		close(t.interrupted)
		return dexco.ToolResult{
			Output:  "Wait interrupted by new input.",
			Success: true,
		}, nil
	case <-t.release:
		return dexco.ToolResult{
			Output:  "Wait completed.",
			Success: true,
		}, nil
	}
}

// Adapted from Codex pending_input `steer_interrupts_wait_agent...` and
// `any_new_input_interrupts_sleep`. Dexco exposes the portable behavior as a
// handler opt-in: pending user input cancels wait/sleep-style tool contexts, the
// tool returns a normal interrupted result, and the queued input is appended at
// the next safe model request.
func TestSessionSteeredInputInterruptsOptInWaitTool(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	interrupted := make(chan struct{})
	release := make(chan struct{})
	router, err := dexco.NewRouter(interruptibleWaitTool{
		started:     started,
		interrupted: interrupted,
		release:     release,
	})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(&pendingInterruptClient{t: t}, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	resultCh := make(chan turnResult, 1)
	go func() {
		result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: "wait for input"},
		}, dexco.NopSink{})
		resultCh <- turnResult{result: result, err: err}
	}()

	waitForClosed(t, started, "interruptible wait tool start")
	if err := session.SteerUserInput(context.Background(), dexco.UserInput{Content: "stop waiting"}); err != nil {
		close(release)
		t.Fatalf("SteerUserInput() error = %v", err)
	}
	waitForClosed(t, interrupted, "interruptible wait tool interruption")

	select {
	case outcome := <-resultCh:
		if outcome.err != nil {
			t.Fatalf("SubmitUserInput() error = %v", outcome.err)
		}
		if outcome.result.ModelCalls != 2 {
			t.Fatalf("ModelCalls = %d, want 2", outcome.result.ModelCalls)
		}
		if outcome.result.FinalMessage != "continued after interrupt" {
			t.Fatalf("FinalMessage = %q, want continued after interrupt", outcome.result.FinalMessage)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatalf("SubmitUserInput did not complete after pending input interrupted the wait tool")
	}
}

type turnResult struct {
	result dexco.TurnResult
	err    error
}

type metadataClient struct{}

func (metadataClient) Stream(context.Context, dexco.Prompt) (dexco.EventStream, error) {
	item := dexco.AssistantMessageItem("metadata observed")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventCreated},
			// Adapted from Codex's response metadata event coverage
			// (`safety_buffering`, model-verification, server-model, and
			// models-etag flows). Dexco is a library and does not own Codex's
			// UI or provider-catalog refresh side effects, but the loop must
			// preserve these events for callers instead of rejecting them as
			// unknown stream input.
			{
				Type: dexco.EventServerModel,
				Metadata: map[string]any{
					"model": "gpt-test",
				},
			},
			{
				Type: dexco.EventModelVerifications,
				Metadata: map[string]any{
					"verified": true,
				},
			},
			{
				Type: dexco.EventTurnModerationMetadata,
				Metadata: map[string]any{
					"category": "safe",
				},
			},
			{
				Type: dexco.EventSafetyBuffering,
				Metadata: map[string]any{
					"use_cases": []any{"cyber"},
				},
			},
			{
				Type: dexco.EventServerReasoningIncluded,
				Metadata: map[string]any{
					"included": true,
				},
			},
			{
				Type:   dexco.EventOutputItemAdded,
				ItemID: "msg-1",
				Item:   &item,
			},
			{
				Type:   dexco.EventToolCallInputDelta,
				ItemID: "call-item-1",
				CallID: "call-1",
				Delta:  `{"cmd":"pwd"}`,
			},
			{
				Type: dexco.EventRateLimits,
				Metadata: map[string]any{
					"remaining_requests": float64(10),
				},
			},
			{
				Type: dexco.EventModelsEtag,
				Metadata: map[string]any{
					"etag": `"models-etag-2"`,
				},
			},
			{
				Type: dexco.EventCompleted,
				TokenUsage: &dexco.TokenUsage{
					InputTokens:  5,
					OutputTokens: 7,
					TotalTokens:  12,
				},
				EndTurn: boolPtr(true),
			},
		},
	}, nil
}

type rawEventSink struct {
	dexco.NopSink
	events []dexco.ResponseEvent
}

func (s *rawEventSink) OnResponseEvent(_ context.Context, _ string, event dexco.ResponseEvent) error {
	s.events = append(s.events, event)
	return nil
}

type clientEventSink struct {
	dexco.NopSink
	events []dexco.ClientEvent
}

func (s *clientEventSink) OnClientEvent(_ context.Context, event dexco.ClientEvent) error {
	s.events = append(s.events, event)
	return nil
}

func TestLibrarySinkCanObserveRawResponseEvents(t *testing.T) {
	t.Parallel()

	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(metadataClient{}, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	sink := &rawEventSink{}
	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "observe metadata"},
	}, sink)
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.Status != dexco.TurnStatusCompleted {
		t.Fatalf("Status = %q, want %q", result.Status, dexco.TurnStatusCompleted)
	}
	got := eventTypes(sink.events)
	want := []dexco.ResponseEventType{
		dexco.EventCreated,
		dexco.EventServerModel,
		dexco.EventModelVerifications,
		dexco.EventTurnModerationMetadata,
		dexco.EventSafetyBuffering,
		dexco.EventServerReasoningIncluded,
		dexco.EventOutputItemAdded,
		dexco.EventToolCallInputDelta,
		dexco.EventRateLimits,
		dexco.EventModelsEtag,
		dexco.EventCompleted,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("raw event types = %#v, want %#v", got, want)
	}
	if sink.events[len(sink.events)-1].TokenUsage.TotalTokens != 12 {
		t.Fatalf("total tokens = %d, want 12", sink.events[len(sink.events)-1].TokenUsage.TotalTokens)
	}
	if sink.events[4].Metadata["use_cases"].([]any)[0] != "cyber" {
		t.Fatalf("safety buffering metadata = %#v, want cyber use case", sink.events[4].Metadata)
	}
}

type webSearchPromptClient struct {
	prompts []dexco.Prompt
}

func (c *webSearchPromptClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

// Adapted from Codex core's web_search request-mode tests. Dexco does not own
// the OpenAI request encoder, but it must resolve the same hosted-web-search
// access semantics into Prompt metadata so model adapters can serialize cached,
// live, indexed, or disabled search consistently.
func TestSessionWebSearchModesResolvePromptAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode dexco.WebSearchMode
		want *dexco.WebSearchRequest
	}{
		{
			name: "empty defaults cached",
			want: &dexco.WebSearchRequest{Mode: dexco.WebSearchModeCached},
		},
		{
			name: "cached",
			mode: dexco.WebSearchModeCached,
			want: &dexco.WebSearchRequest{Mode: dexco.WebSearchModeCached},
		},
		{
			name: "live",
			mode: dexco.WebSearchModeLive,
			want: &dexco.WebSearchRequest{
				Mode:              dexco.WebSearchModeLive,
				ExternalWebAccess: true,
			},
		},
		{
			name: "indexed",
			mode: dexco.WebSearchModeIndexed,
			want: &dexco.WebSearchRequest{
				Mode:                dexco.WebSearchModeIndexed,
				ExternalWebAccess:   true,
				IndexGatedWebAccess: true,
			},
		},
		{
			name: "disabled",
			mode: dexco.WebSearchModeDisabled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &webSearchPromptClient{}
			router, err := dexco.NewRouter()
			if err != nil {
				t.Fatalf("NewRouter() error = %v", err)
			}
			turnRunner, err := dexco.NewRunner(client, router)
			if err != nil {
				t.Fatalf("NewRunner() error = %v", err)
			}
			session, err := dexco.NewSession(dexco.Config{
				WebSearch: &dexco.WebSearchRequest{Mode: tt.mode},
			}, turnRunner)
			if err != nil {
				t.Fatalf("NewSession() error = %v", err)
			}

			_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
				Input: dexco.UserInput{Content: "search mode"},
			}, dexco.NopSink{})
			if err != nil {
				t.Fatalf("SubmitUserInput() error = %v", err)
			}

			if got := client.prompts[0].WebSearch; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Prompt.WebSearch = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// Adapted from Codex's web_search config forwarding and between-turn update
// coverage. Dexco forwards provider-neutral equivalents of search_context_size,
// filters.allowed_domains, user_location, and lets a turn override the session
// default without committing that metadata to history.
func TestSessionForwardsWebSearchConfigAndTurnOverride(t *testing.T) {
	t.Parallel()

	client := &webSearchPromptClient{}
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{
		WebSearch: &dexco.WebSearchRequest{
			Mode:              dexco.WebSearchModeLive,
			SearchContextSize: "high",
			AllowedDomains:    []string{"example.com"},
			UserLocation: &dexco.WebSearchUserLocation{
				Country:  "US",
				City:     "New York",
				Timezone: "America/New_York",
			},
		},
	}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "configured search"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(first) error = %v", err)
	}
	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "indexed search"},
		WebSearch: &dexco.WebSearchRequest{
			Mode:           dexco.WebSearchModeIndexed,
			AllowedDomains: []string{"docs.example.com"},
		},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(second) error = %v", err)
	}

	wantFirst := &dexco.WebSearchRequest{
		Mode:              dexco.WebSearchModeLive,
		ExternalWebAccess: true,
		SearchContextSize: "high",
		AllowedDomains:    []string{"example.com"},
		UserLocation: &dexco.WebSearchUserLocation{
			Country:  "US",
			City:     "New York",
			Timezone: "America/New_York",
		},
	}
	if got := client.prompts[0].WebSearch; !reflect.DeepEqual(got, wantFirst) {
		t.Fatalf("first Prompt.WebSearch = %#v, want %#v", got, wantFirst)
	}
	wantSecond := &dexco.WebSearchRequest{
		Mode:                dexco.WebSearchModeIndexed,
		ExternalWebAccess:   true,
		IndexGatedWebAccess: true,
		AllowedDomains:      []string{"docs.example.com"},
	}
	if got := client.prompts[1].WebSearch; !reflect.DeepEqual(got, wantSecond) {
		t.Fatalf("second Prompt.WebSearch = %#v, want %#v", got, wantSecond)
	}
}

type webSearchClient struct {
	items []dexco.Item
}

func (c webSearchClient) Stream(context.Context, dexco.Prompt) (dexco.EventStream, error) {
	events := make([]dexco.ResponseEvent, 0, len(c.items)+1)
	for _, item := range c.items {
		item := item
		events = append(events, dexco.ResponseEvent{
			Type: dexco.EventOutputItemDone,
			Item: &item,
		})
	}
	events = append(events, dexco.ResponseEvent{Type: dexco.EventCompleted, EndTurn: boolPtr(true)})
	return &sliceStream{events: events}, nil
}

// Adapted from Codex event_mapping web-search tests. Dexco model clients
// already emit normalized items instead of raw Responses API JSON, but the loop
// still needs to preserve hosted web-search actions as first-class history and
// client events.
func TestRunnerCommitsWebSearchItemsAndEmitsClientEvents(t *testing.T) {
	t.Parallel()

	webSearchItems := []dexco.Item{
		dexco.WebSearchItem("ws_1", "completed", dexco.WebSearchAction{
			Kind:  dexco.WebSearchActionSearch,
			Query: "weather",
		}),
		dexco.WebSearchItem("ws_open", "completed", dexco.WebSearchAction{
			Kind: dexco.WebSearchActionOpenPage,
			URL:  "https://example.com",
		}),
		dexco.WebSearchItem("ws_find", "completed", dexco.WebSearchAction{
			Kind:    dexco.WebSearchActionFindInPage,
			URL:     "https://example.com",
			Pattern: "needle",
		}),
		dexco.WebSearchItem("ws_partial", "in_progress", dexco.WebSearchAction{
			Kind: dexco.WebSearchActionOther,
		}),
	}
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(webSearchClient{items: webSearchItems}, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	sink := &clientEventSink{}
	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "search"},
	}, sink)
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	wantHistory := append([]dexco.Item{dexco.UserInputItem("search")}, webSearchItems...)
	if !reflect.DeepEqual(result.History, wantHistory) {
		t.Fatalf("History = %#v, want %#v", result.History, wantHistory)
	}
	gotEvents := webSearchEvents(sink.events)
	if !reflect.DeepEqual(gotEvents, webSearchItems) {
		t.Fatalf("web search client events = %#v, want %#v", gotEvents, webSearchItems)
	}
	if result.History[3].WebSearch.Query != "'needle' in https://example.com" {
		t.Fatalf("find-in-page query = %q, want formatted query", result.History[3].WebSearch.Query)
	}
	if result.History[4].WebSearch.Query != "" {
		t.Fatalf("partial query = %q, want empty", result.History[4].WebSearch.Query)
	}
}

type imageGenerationClient struct {
	items []dexco.Item
}

func (c imageGenerationClient) Stream(context.Context, dexco.Prompt) (dexco.EventStream, error) {
	events := make([]dexco.ResponseEvent, 0, len(c.items)+1)
	for _, item := range c.items {
		item := item
		events = append(events, dexco.ResponseEvent{
			Type: dexco.EventOutputItemDone,
			Item: &item,
		})
	}
	events = append(events, dexco.ResponseEvent{Type: dexco.EventCompleted, EndTurn: boolPtr(true)})
	return &sliceStream{events: events}, nil
}

// Adapted from Codex core's image_generation_call item tests. Dexco does not
// save generated image artifacts itself, but hosted image-generation calls must
// still be committed as first-class history and emitted to clients with the
// same status, revised prompt, result, and optional saved-path metadata.
func TestRunnerCommitsImageGenerationItemsAndEmitsClientEvents(t *testing.T) {
	t.Parallel()

	imageGenerationItems := []dexco.Item{
		dexco.ImageGenerationItem(
			"ig_image_saved_to_temp_dir_default",
			"generating",
			"A tiny blue square",
			"Zm9v",
			"/tmp/generated.png",
		),
		dexco.ImageGenerationItem("ig_invalid", "completed", "broken payload", "_-8", ""),
	}
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(imageGenerationClient{items: imageGenerationItems}, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	sink := &clientEventSink{}
	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "generate an image"},
	}, sink)
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	wantHistory := append([]dexco.Item{dexco.UserInputItem("generate an image")}, imageGenerationItems...)
	if !reflect.DeepEqual(result.History, wantHistory) {
		t.Fatalf("History = %#v, want %#v", result.History, wantHistory)
	}
	if !result.Metrics.HasFirstOutput {
		t.Fatalf("Metrics.HasFirstOutput = false, want image generation to count as model output")
	}
	gotEvents := imageGenerationEvents(sink.events)
	if !reflect.DeepEqual(gotEvents, imageGenerationItems) {
		t.Fatalf("image generation client events = %#v, want %#v", gotEvents, imageGenerationItems)
	}
	if result.History[2].ImageGeneration.SavedPath != "" {
		t.Fatalf("invalid image saved path = %q, want empty", result.History[2].ImageGeneration.SavedPath)
	}
}

type hookPromptClient struct {
	item dexco.Item
}

func (c hookPromptClient) Stream(context.Context, dexco.Prompt) (dexco.EventStream, error) {
	item := c.item
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

// Adapted from Codex event_mapping hook prompt tests. Dexco does not expose
// Codex's raw Responses parser, but it preserves normalized hook prompts as
// distinct turn items and client events rather than collapsing them into normal
// user text.
func TestRunnerCommitsHookPromptItemAndEmitsClientEvent(t *testing.T) {
	t.Parallel()

	hookPrompt := dexco.HookPromptItem("msg-1", []dexco.HookPromptFragment{
		{Text: "Retry with exactly the phrase meow meow meow.", HookRunID: "hook-run-1"},
	})
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(hookPromptClient{item: hookPrompt}, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	sink := &clientEventSink{}
	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "run hook"},
	}, sink)
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	wantHistory := []dexco.Item{dexco.UserInputItem("run hook"), hookPrompt}
	if !reflect.DeepEqual(result.History, wantHistory) {
		t.Fatalf("History = %#v, want %#v", result.History, wantHistory)
	}
	gotEvents := hookPromptEvents(sink.events)
	if !reflect.DeepEqual(gotEvents, []dexco.Item{hookPrompt}) {
		t.Fatalf("hook prompt client events = %#v, want %#v", gotEvents, []dexco.Item{hookPrompt})
	}
}

type retryClient struct {
	calls int
}

func (c *retryClient) Stream(context.Context, dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	if c.calls == 1 {
		return nil, errors.New("temporary outage")
	}
	item := dexco.AssistantMessageItem("recovered")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

type recvRetryClient struct {
	t     *testing.T
	calls int
}

func (c *recvRetryClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	switch c.calls {
	case 1:
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "failed-call",
			Name:      "counting_tool",
			Arguments: json.RawMessage(`{}`),
		})
		return &errorAfterEventsStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputTextDelta, Delta: "partial"},
				{Type: dexco.EventOutputItemDone, Item: &item},
			},
			err: errors.New("stream interrupted"),
		}, nil
	case 2:
		if !containsToolCall(prompt.History, "failed-call") ||
			!containsToolResult(prompt.History, "failed-call", "called") {
			c.t.Fatalf("retry prompt missing failed-attempt tool transcript: %#v", prompt.History)
		}
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputTextDelta, Delta: "stable"},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", c.calls)
		return nil, nil
	}
}

type errorAfterEventsStream struct {
	events []dexco.ResponseEvent
	index  int
	err    error
}

func (s *errorAfterEventsStream) Recv() (dexco.ResponseEvent, error) {
	if s.index >= len(s.events) {
		return dexco.ResponseEvent{}, s.err
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

type countingTool struct {
	calls int
}

func (t *countingTool) Name() string {
	return "counting_tool"
}

func (t *countingTool) Spec() dexco.ToolSpec {
	return dexco.ToolSpec{Name: "counting_tool"}
}

func (t *countingTool) Call(context.Context, dexco.ToolCall) (dexco.ToolResult, error) {
	t.calls++
	return dexco.ToolResult{Output: "called", Success: true}, nil
}

type visibleAttemptSink struct {
	dexco.NopSink
	text      string
	toolCalls []dexco.ToolCall
	events    []dexco.ClientEvent
}

func (s *visibleAttemptSink) OnTextDelta(_ context.Context, _ string, delta string) error {
	s.text += delta
	return nil
}

func (s *visibleAttemptSink) OnToolCall(_ context.Context, _ string, call dexco.ToolCall) error {
	s.toolCalls = append(s.toolCalls, call)
	return nil
}

func (s *visibleAttemptSink) OnClientEvent(_ context.Context, event dexco.ClientEvent) error {
	s.events = append(s.events, event)
	return nil
}

// Adapted from Codex's in-flight tool retry behavior. If a stream fails after a
// completed tool-call item has started execution, Codex drains and records that
// tool output before retrying. Text deltas from the failed attempt stay hidden,
// but the retry prompt must include the tool transcript so side effects are not
// forgotten or duplicated.
func TestRunnerRetriesReceiveFailureWithStartedToolTranscript(t *testing.T) {
	t.Parallel()

	tool := &countingTool{}
	router, err := dexco.NewRouter(tool)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &recvRetryClient{t: t}
	turnRunner, err := dexco.NewRunnerWithOptions(client, router, dexco.RunnerOptions{
		MaxModelRetries: 1,
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	sink := &visibleAttemptSink{}
	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "retry recv"},
	}, sink)
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.ModelCalls != 2 {
		t.Fatalf("ModelCalls = %d, want 2", result.ModelCalls)
	}
	if result.FinalMessage != "stable" {
		t.Fatalf("FinalMessage = %q, want %q", result.FinalMessage, "stable")
	}
	if sink.text != "stable" {
		t.Fatalf("visible text = %q, want only successful attempt text %q", sink.text, "stable")
	}
	if len(sink.toolCalls) != 0 {
		t.Fatalf("visible tool calls = %#v, want none from failed attempt", sink.toolCalls)
	}
	if tool.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.calls)
	}
	if !containsToolCall(result.History, "failed-call") ||
		!containsToolResult(result.History, "failed-call", "called") {
		t.Fatalf("final history missing failed-attempt tool transcript: %#v", result.History)
	}
	if !hasClientEvent(sink.events, dexco.ClientEventModelRetry) {
		t.Fatalf("client events missing retry event: %#v", clientEventTypes(sink.events))
	}
}

type nonRetryableErrorClient struct {
	err   error
	calls int
}

func (c *nonRetryableErrorClient) Stream(context.Context, dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	return nil, c.err
}

// Adapted from Codex's cyber-policy failure coverage. Provider adapters can
// normalize policy failures into ModelError; the runner must not retry them even
// when generic retry support is enabled.
func TestRunnerCyberPolicyErrorIsTypedAndNotRetried(t *testing.T) {
	t.Parallel()

	client := &nonRetryableErrorClient{
		err: dexco.NewModelError(
			dexco.ModelErrorCyberPolicy,
			"This request has been flagged for potentially high-risk cyber activity.",
			false,
		),
	}
	session := newRetryPolicyTestSession(t, client)
	sink := &clientEventSink{}
	_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "trigger cyber policy"},
	}, sink)
	if err == nil {
		t.Fatalf("SubmitUserInput() error = nil, want cyber policy error")
	}
	var modelErr *dexco.ModelError
	if !errors.As(err, &modelErr) {
		t.Fatalf("error = %v, want ModelError", err)
	}
	if modelErr.Kind != dexco.ModelErrorCyberPolicy || modelErr.Retryable {
		t.Fatalf("ModelError = %#v, want non-retryable cyber policy", modelErr)
	}
	if client.calls != 1 {
		t.Fatalf("model calls = %d, want 1", client.calls)
	}
	if hasClientEvent(sink.events, dexco.ClientEventModelRetry) {
		t.Fatalf("retry event emitted for non-retryable error: %#v", sink.events)
	}
}

type quotaStreamClient struct {
	calls int
}

func (c *quotaStreamClient) Stream(context.Context, dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	return &quotaFailureStream{}, nil
}

type quotaFailureStream struct {
	created bool
}

func (s *quotaFailureStream) Recv() (dexco.ResponseEvent, error) {
	if !s.created {
		s.created = true
		return dexco.ResponseEvent{Type: dexco.EventCreated}, nil
	}
	return dexco.ResponseEvent{}, dexco.NewModelError(
		dexco.ModelErrorQuota,
		"Quota exceeded. Check your plan and billing details.",
		false,
	)
}

// Adapted from Codex's quota-exceeded event coverage. Dexco surfaces the typed
// error through the library return path and guarantees it is not retried into
// duplicate failures.
func TestRunnerQuotaExceededIsTypedAndNotRetried(t *testing.T) {
	t.Parallel()

	client := &quotaStreamClient{}
	session := newRetryPolicyTestSession(t, client)
	sink := &clientEventSink{}
	_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "quota?"},
	}, sink)
	if err == nil {
		t.Fatalf("SubmitUserInput() error = nil, want quota error")
	}
	var modelErr *dexco.ModelError
	if !errors.As(err, &modelErr) {
		t.Fatalf("error = %v, want ModelError", err)
	}
	if modelErr.Kind != dexco.ModelErrorQuota || modelErr.Retryable {
		t.Fatalf("ModelError = %#v, want non-retryable quota", modelErr)
	}
	if client.calls != 1 {
		t.Fatalf("model calls = %d, want 1", client.calls)
	}
	if hasClientEvent(sink.events, dexco.ClientEventModelRetry) {
		t.Fatalf("retry event emitted for quota error: %#v", sink.events)
	}
}

// Adapted from Codex core's stream_no_completed test. If the provider stream
// closes before response.completed, partial deltas must not become visible or
// committed; the runner retries from the last stable prompt.
type earlyEOFRetryClient struct {
	t     *testing.T
	calls int
}

func (c *earlyEOFRetryClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	if c.calls == 1 {
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputTextDelta, Delta: "partial"},
			},
		}, nil
	}
	if containsAssistantMessage(prompt.History, "partial") {
		c.t.Fatalf("retry prompt included failed partial assistant output: %#v", prompt.History)
	}
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputTextDelta, Delta: "complete"},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

func TestRunnerRetriesEarlyEOFBeforeCompleted(t *testing.T) {
	t.Parallel()

	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &earlyEOFRetryClient{t: t}
	turnRunner, err := dexco.NewRunnerWithOptions(client, router, dexco.RunnerOptions{
		MaxModelRetries: 1,
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	sink := &visibleAttemptSink{}
	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "retry early eof"},
	}, sink)
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.ModelCalls != 2 {
		t.Fatalf("ModelCalls = %d, want 2", result.ModelCalls)
	}
	if result.FinalMessage != "complete" {
		t.Fatalf("FinalMessage = %q, want complete", result.FinalMessage)
	}
	if sink.text != "complete" {
		t.Fatalf("visible text = %q, want only successful attempt text %q", sink.text, "complete")
	}
	if containsAssistantMessage(result.History, "partial") {
		t.Fatalf("final history includes failed partial output: %#v", result.History)
	}
}

func TestRunnerRetriesModelStreamWhenConfigured(t *testing.T) {
	t.Parallel()

	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &retryClient{}
	turnRunner, err := dexco.NewRunnerWithOptions(client, router, dexco.RunnerOptions{
		MaxModelRetries: 1,
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	sink := &clientEventSink{}
	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "retry"},
	}, sink)
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.ModelCalls != 2 {
		t.Fatalf("ModelCalls = %d, want 2", result.ModelCalls)
	}
	if result.FinalMessage != "recovered" {
		t.Fatalf("FinalMessage = %q, want %q", result.FinalMessage, "recovered")
	}
	if !hasClientEvent(sink.events, dexco.ClientEventModelRetry) {
		t.Fatalf("client events missing retry event: %#v", clientEventTypes(sink.events))
	}
}

type hookClient struct {
	prompts []dexco.Prompt
	calls   int
}

func (c *hookClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	c.calls++
	if c.calls == 1 {
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "call-1",
			Name:      "echo_tool",
			Arguments: json.RawMessage(`{"value":"original"}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	}
	item := dexco.AssistantMessageItem("hooked done")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

type lifecycleClient struct {
	t     *testing.T
	calls int
}

func (c *lifecycleClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	if c.calls == 1 {
		completed := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "completed-call",
			Name:      "unsuccessful_tool",
			Arguments: json.RawMessage(`{}`),
		})
		failed := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "failed-call",
			Name:      "handler_error_tool",
			Arguments: json.RawMessage(`{}`),
		})
		missing := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "missing-call",
			Name:      "missing_tool",
			Arguments: json.RawMessage(`{}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &completed},
				{Type: dexco.EventOutputItemDone, Item: &failed},
				{Type: dexco.EventOutputItemDone, Item: &missing},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	}

	if !containsToolResult(prompt.History, "completed-call", "completed without success") ||
		!containsToolResult(prompt.History, "failed-call", "handler exploded") ||
		!containsToolResult(prompt.History, "missing-call", `unknown tool "missing_tool"`) {
		c.t.Fatalf("follow-up prompt missing lifecycle tool results: %#v", prompt.History)
	}
	item := dexco.AssistantMessageItem("lifecycle done")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

type unsuccessfulTool struct{}

func (unsuccessfulTool) Name() string {
	return "unsuccessful_tool"
}

func (unsuccessfulTool) Spec() dexco.ToolSpec {
	return dexco.ToolSpec{Name: "unsuccessful_tool"}
}

func (unsuccessfulTool) Call(context.Context, dexco.ToolCall) (dexco.ToolResult, error) {
	return dexco.ToolResult{Output: "completed without success", Success: false}, nil
}

type handlerErrorTool struct{}

func (handlerErrorTool) Name() string {
	return "handler_error_tool"
}

func (handlerErrorTool) Spec() dexco.ToolSpec {
	return dexco.ToolSpec{Name: "handler_error_tool"}
}

func (handlerErrorTool) Call(context.Context, dexco.ToolCall) (dexco.ToolResult, error) {
	return dexco.ToolResult{}, errors.New("handler exploded")
}

type lifecycleRecord struct {
	phase           dexco.ToolLifecyclePhase
	callID          string
	name            string
	outcome         dexco.ToolLifecycleOutcome
	success         bool
	handlerExecuted bool
	output          string
}

func lifecycleRecordFromEvent(event dexco.ToolLifecycleEvent) lifecycleRecord {
	record := lifecycleRecord{
		phase:           event.Phase,
		callID:          event.Call.CallID,
		name:            event.Call.Name,
		outcome:         event.Outcome,
		success:         event.Success,
		handlerExecuted: event.HandlerExecuted,
	}
	if event.Result != nil {
		record.output = event.Result.Output
	}
	return record
}

func lifecycleRecordsByCall(records []lifecycleRecord) map[string][]lifecycleRecord {
	byCall := make(map[string][]lifecycleRecord)
	for _, record := range records {
		byCall[record.callID] = append(byCall[record.callID], record)
	}
	return byCall
}

// Adapted from Codex core's
// `core/src/tools/registry_tests.rs::dispatch_notifies_tool_lifecycle_contributors`
// and unsupported-tool lifecycle coverage in `tool_dispatch_trace_tests.rs`.
// Dexco exposes lifecycle contributors as library hooks, but preserves Codex's
// important outcome distinction: a handler can complete with success=false, it
// can execute and fail before producing its own result, or dispatch can fail
// before any handler runs.
func TestRunnerToolLifecycleHookRecordsCompletedAndFailedOutcomes(t *testing.T) {
	t.Parallel()

	router, err := dexco.NewRouter(unsuccessfulTool{}, handlerErrorTool{})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &lifecycleClient{t: t}
	var recordsMu sync.Mutex
	var records []lifecycleRecord
	turnRunner, err := dexco.NewRunnerWithOptions(client, router, dexco.RunnerOptions{
		Hooks: dexco.Hooks{
			ToolLifecycle: func(_ context.Context, _ dexco.Turn, event dexco.ToolLifecycleEvent) error {
				recordsMu.Lock()
				defer recordsMu.Unlock()
				records = append(records, lifecycleRecordFromEvent(event))
				return nil
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "call lifecycle tools"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "lifecycle done" {
		t.Fatalf("FinalMessage = %q, want lifecycle done", result.FinalMessage)
	}
	want := map[string][]lifecycleRecord{
		"completed-call": {
			{phase: dexco.ToolLifecycleStart, callID: "completed-call", name: "unsuccessful_tool"},
			{
				phase:           dexco.ToolLifecycleFinish,
				callID:          "completed-call",
				name:            "unsuccessful_tool",
				outcome:         dexco.ToolLifecycleOutcomeCompleted,
				success:         false,
				handlerExecuted: true,
				output:          "completed without success",
			},
		},
		"failed-call": {
			{phase: dexco.ToolLifecycleStart, callID: "failed-call", name: "handler_error_tool"},
			{
				phase:           dexco.ToolLifecycleFinish,
				callID:          "failed-call",
				name:            "handler_error_tool",
				outcome:         dexco.ToolLifecycleOutcomeFailed,
				success:         false,
				handlerExecuted: true,
				output:          "handler exploded",
			},
		},
		"missing-call": {
			{phase: dexco.ToolLifecycleStart, callID: "missing-call", name: "missing_tool"},
			{
				phase:           dexco.ToolLifecycleFinish,
				callID:          "missing-call",
				name:            "missing_tool",
				outcome:         dexco.ToolLifecycleOutcomeFailed,
				success:         false,
				handlerExecuted: false,
				output:          `unknown tool "missing_tool"`,
			},
		},
	}
	recordsMu.Lock()
	got := lifecycleRecordsByCall(records)
	recordsMu.Unlock()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle records = %#v, want %#v", got, want)
	}
}

func TestRunnerOptionsHooksCanModifyPromptAndToolResult(t *testing.T) {
	t.Parallel()

	router, err := dexco.NewRouter(echoTool{})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &hookClient{}
	turnRunner, err := dexco.NewRunnerWithOptions(client, router, dexco.RunnerOptions{
		Hooks: dexco.Hooks{
			BeforeModelRequest: func(_ context.Context, _ dexco.Turn, prompt dexco.Prompt) (dexco.Prompt, error) {
				prompt.Instructions = "hooked instructions"
				return prompt, nil
			},
			BeforeToolCall: func(_ context.Context, _ dexco.Turn, call dexco.ToolCall) (dexco.ToolCall, error) {
				call.Arguments = json.RawMessage(`{"value":"hooked"}`)
				return call, nil
			},
			AfterToolCall: func(_ context.Context, _ dexco.Turn, _ dexco.ToolCall, item dexco.Item) (dexco.Item, error) {
				item.ToolResult.Output += "|after"
				return item, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "hook"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "hooked done" {
		t.Fatalf("FinalMessage = %q, want %q", result.FinalMessage, "hooked done")
	}
	if client.prompts[0].Instructions != "hooked instructions" {
		t.Fatalf("prompt instructions = %q, want hooked instructions", client.prompts[0].Instructions)
	}
	if !containsToolResult(client.prompts[1].History, "call-1", "hooked|after") {
		t.Fatalf("second prompt missing hooked tool result: %#v", client.prompts[1].History)
	}
}

type truncatingToolOutputClient struct {
	t     *testing.T
	calls int
	seen  *dexco.ToolResult
}

func (c *truncatingToolOutputClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	if c.calls == 1 {
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "long-call",
			Name:      "long_output_tool",
			Arguments: json.RawMessage(`{}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	}

	var got *dexco.ToolResult
	for _, item := range prompt.History {
		if item.ToolResult != nil && item.ToolResult.CallID == "long-call" {
			result := *item.ToolResult
			got = &result
			break
		}
	}
	if got == nil {
		c.t.Fatalf("second prompt missing long-call tool result: %#v", prompt.History)
	}
	c.seen = got
	if got.Name != "long_output_tool" || !got.Success {
		c.t.Fatalf("tool result metadata = %#v, want name and success preserved", got)
	}
	if !strings.Contains(got.Output, "Warning: truncated tool output") ||
		!strings.Contains(got.Output, "characters truncated") {
		c.t.Fatalf("tool output = %q, want truncation marker", got.Output)
	}
	if !strings.Contains(got.Output, "ABCDEFGH") ||
		!strings.Contains(got.Output, "UVWXYZ12") ||
		strings.Contains(got.Output, "middlemiddle") {
		c.t.Fatalf("tool output = %q, want head/tail without middle", got.Output)
	}

	item := dexco.AssistantMessageItem("truncated done")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

// Adapted from Codex ContextManager history tests for function/custom-tool
// output truncation. Dexco does not own the OpenAI ResponseItem enum, so it
// enforces the same bound at the provider-neutral ToolResult boundary before
// replaying tool output into the next model prompt.
func TestRunnerTruncatesToolResultOutputBeforeFollowUpPrompt(t *testing.T) {
	t.Parallel()

	longOutput := "ABCDEFGH" + strings.Repeat("middle", 20) + "UVWXYZ12"
	router, err := dexco.NewRouter(namedOutputTool{
		name:        "long_output_tool",
		description: "Returns a long output.",
		output:      longOutput,
	})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &truncatingToolOutputClient{t: t}
	turnRunner, err := dexco.NewRunnerWithOptions(client, router, dexco.RunnerOptions{
		ToolResultMaxChars: 16,
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "run long tool"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "truncated done" {
		t.Fatalf("FinalMessage = %q, want truncated done", result.FinalMessage)
	}
	if client.seen == nil {
		t.Fatalf("client did not observe truncated tool result")
	}
	if containsToolResult(result.History, "long-call", longOutput) {
		t.Fatalf("final history retained unbounded tool output")
	}
	if !containsToolResult(result.History, "long-call", client.seen.Output) {
		t.Fatalf("final history missing truncated tool output: %#v", result.History)
	}
}

// Adapted from Codex's UserPromptSubmit hook tests. Codex can block a prompt
// before sampling and must not commit that rejected prompt to the model-visible
// transcript. Dexco exposes the same library invariant through
// BeforeModelRequest: hook errors stop sampling and Session only persists a turn
// after the runner returns a completed result.
type promptRecordingClient struct {
	prompts []dexco.Prompt
	calls   int
}

func (c *promptRecordingClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	c.calls++
	item := dexco.AssistantMessageItem("accepted")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

func TestBeforeModelRequestHookErrorSkipsModelAndDoesNotPersistInput(t *testing.T) {
	t.Parallel()

	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &promptRecordingClient{}
	hookCalls := 0
	turnRunner, err := dexco.NewRunnerWithOptions(client, router, dexco.RunnerOptions{
		Hooks: dexco.Hooks{
			BeforeModelRequest: func(_ context.Context, _ dexco.Turn, prompt dexco.Prompt) (dexco.Prompt, error) {
				hookCalls++
				if hookCalls == 1 {
					return dexco.Prompt{}, errors.New("blocked by hook")
				}
				return prompt, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "blocked input"},
	}, dexco.NopSink{})
	if err == nil || err.Error() != "before model request hook: blocked by hook" {
		t.Fatalf("first SubmitUserInput() error = %v, want blocked hook error", err)
	}
	if client.calls != 0 {
		t.Fatalf("model calls after blocked hook = %d, want 0", client.calls)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "allowed input"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("second SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "accepted" {
		t.Fatalf("FinalMessage = %q, want accepted", result.FinalMessage)
	}
	if client.calls != 1 {
		t.Fatalf("model calls = %d, want 1", client.calls)
	}
	if containsUserInput(client.prompts[0].History, "blocked input") {
		t.Fatalf("allowed prompt leaked blocked input into history: %#v", client.prompts[0].History)
	}
	if !containsUserInput(client.prompts[0].History, "allowed input") {
		t.Fatalf("allowed prompt missing current user input: %#v", client.prompts[0].History)
	}
}

// Adapted from Codex hook execution around model requests and retry handling.
// Codex surfaces each failed provider attempt to lifecycle observers while
// retrying from the same stable prompt. Dexco keeps that as AfterModelRequest:
// callers can observe the stream-start error, then the successful retry.
func TestAfterModelRequestHookObservesRetryAttempts(t *testing.T) {
	t.Parallel()

	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &retryClient{}
	var observed []string
	turnRunner, err := dexco.NewRunnerWithOptions(client, router, dexco.RunnerOptions{
		MaxModelRetries: 1,
		Hooks: dexco.Hooks{
			AfterModelRequest: func(_ context.Context, _ dexco.Turn, _ dexco.Prompt, err error) error {
				if err == nil {
					observed = append(observed, "")
					return nil
				}
				observed = append(observed, err.Error())
				return nil
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "retry with hook"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "recovered" {
		t.Fatalf("FinalMessage = %q, want recovered", result.FinalMessage)
	}
	want := []string{"temporary outage", ""}
	if !reflect.DeepEqual(observed, want) {
		t.Fatalf("observed model hook errors = %#v, want %#v", observed, want)
	}
}

// Adapted from Codex PreToolUse blocking behavior. Codex blocks before the
// side-effecting handler runs; Dexco's in-process equivalent is returning an
// error from BeforeToolCall, which must skip Dispatch entirely.
type oneToolCallClient struct {
	toolName string
	calls    int
}

func (c *oneToolCallClient) Stream(context.Context, dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	item := dexco.ToolCallItem(dexco.ToolCall{
		CallID:    "call-1",
		Name:      c.toolName,
		Arguments: json.RawMessage(`{}`),
	})
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

func TestBeforeToolCallHookErrorSkipsToolDispatch(t *testing.T) {
	t.Parallel()

	tool := &countingTool{}
	router, err := dexco.NewRouter(tool)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &oneToolCallClient{toolName: "counting_tool"}
	turnRunner, err := dexco.NewRunnerWithOptions(client, router, dexco.RunnerOptions{
		Hooks: dexco.Hooks{
			BeforeToolCall: func(context.Context, dexco.Turn, dexco.ToolCall) (dexco.ToolCall, error) {
				return dexco.ToolCall{}, errors.New("blocked by pre tool hook")
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "call tool"},
	}, dexco.NopSink{})
	if err == nil || err.Error() != `before tool call hook "counting_tool": blocked by pre tool hook` {
		t.Fatalf("SubmitUserInput() error = %v, want pre-tool hook error", err)
	}
	if client.calls != 1 {
		t.Fatalf("model calls = %d, want 1", client.calls)
	}
	if tool.calls != 0 {
		t.Fatalf("tool calls = %d, want 0", tool.calls)
	}
}

// Adapted from Codex PostToolUse blocking behavior. PostToolUse runs only after
// the tool has completed, and a blocking/error result must stop the follow-up
// model request so the caller can decide how to handle the failed hook.
func TestAfterToolCallHookErrorStopsBeforeFollowupSampling(t *testing.T) {
	t.Parallel()

	tool := &countingTool{}
	router, err := dexco.NewRouter(tool)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &oneToolCallClient{toolName: "counting_tool"}
	turnRunner, err := dexco.NewRunnerWithOptions(client, router, dexco.RunnerOptions{
		Hooks: dexco.Hooks{
			AfterToolCall: func(context.Context, dexco.Turn, dexco.ToolCall, dexco.Item) (dexco.Item, error) {
				return dexco.Item{}, errors.New("blocked by post tool hook")
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "call tool"},
	}, dexco.NopSink{})
	if err == nil || err.Error() != `after tool call hook "counting_tool": blocked by post tool hook` {
		t.Fatalf("SubmitUserInput() error = %v, want post-tool hook error", err)
	}
	if client.calls != 1 {
		t.Fatalf("model calls = %d, want 1", client.calls)
	}
	if tool.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.calls)
	}
}

type guardrailClient struct {
	t              *testing.T
	wantToolOutput string
	finalMessage   string
	calls          int
}

func (c *guardrailClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	if c.calls == 1 {
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "call-1",
			Name:      "sensitive_tool",
			Arguments: json.RawMessage(`{"path":"workspace/file.txt"}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	}
	if !containsToolResult(prompt.History, "call-1", c.wantToolOutput) {
		c.t.Fatalf("prompt missing guarded tool output %q: %#v", c.wantToolOutput, prompt.History)
	}
	item := dexco.AssistantMessageItem(c.finalMessage)
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

type sensitiveTool struct {
	calls int
}

func (t *sensitiveTool) Name() string {
	return "sensitive_tool"
}

func (t *sensitiveTool) Spec() dexco.ToolSpec {
	return dexco.ToolSpec{Name: "sensitive_tool"}
}

func (t *sensitiveTool) Guardrail(context.Context, dexco.ToolCall) (dexco.ToolGuardrail, error) {
	return dexco.ToolGuardrail{
		Risk:                dexco.ToolRiskWorkspaceWrite,
		ApprovalRequirement: dexco.ApprovalRequirementRequired,
		Reason:              "write workspace/file.txt",
	}, nil
}

func (t *sensitiveTool) Call(context.Context, dexco.ToolCall) (dexco.ToolResult, error) {
	t.calls++
	return dexco.ToolResult{Output: "approved-output", Success: true}, nil
}

func TestGuardrailHookDeniesBeforeReviewerAndSkipsTool(t *testing.T) {
	t.Parallel()

	tool := &sensitiveTool{}
	router, err := dexco.NewRouter(tool)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &guardrailClient{
		t:              t,
		wantToolOutput: "tool call denied by guardrail: permission hook denied tool call",
		finalMessage:   "denial handled",
	}
	reviewerCalls := 0
	turnRunner, err := dexco.NewRunnerWithOptions(client, router, dexco.RunnerOptions{
		Hooks: dexco.Hooks{
			ReviewToolCall: func(_ context.Context, _ dexco.Turn, request dexco.ToolApprovalRequest) (dexco.ApprovalDecision, error) {
				if request.Reason != "write workspace/file.txt" {
					t.Fatalf("approval reason = %q, want write workspace/file.txt", request.Reason)
				}
				return dexco.ApprovalDecisionDenied, nil
			},
		},
		Guardrails: dexco.Guardrails{
			ApprovalPolicy: dexco.ApprovalPolicyRequireForSensitive,
			Reviewer: func(context.Context, dexco.Turn, dexco.ToolApprovalRequest) (dexco.ApprovalDecision, error) {
				reviewerCalls++
				return dexco.ApprovalDecisionApproved, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	sink := &clientEventSink{}
	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "guard"},
	}, sink)
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "denial handled" {
		t.Fatalf("FinalMessage = %q, want denial handled", result.FinalMessage)
	}
	if tool.calls != 0 {
		t.Fatalf("tool calls = %d, want 0", tool.calls)
	}
	if reviewerCalls != 0 {
		t.Fatalf("reviewer calls = %d, want 0", reviewerCalls)
	}
	if !hasClientEvent(sink.events, dexco.ClientEventToolApprovalDecision) {
		t.Fatalf("client events missing approval decision: %#v", clientEventTypes(sink.events))
	}
}

func TestGuardrailReviewerApprovesRequiredTool(t *testing.T) {
	t.Parallel()

	tool := &sensitiveTool{}
	router, err := dexco.NewRouter(tool)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &guardrailClient{
		t:              t,
		wantToolOutput: "approved-output",
		finalMessage:   "approval handled",
	}
	hookCalls := 0
	reviewerCalls := 0
	turnRunner, err := dexco.NewRunnerWithOptions(client, router, dexco.RunnerOptions{
		Hooks: dexco.Hooks{
			ReviewToolCall: func(context.Context, dexco.Turn, dexco.ToolApprovalRequest) (dexco.ApprovalDecision, error) {
				hookCalls++
				return dexco.ApprovalDecisionNoDecision, nil
			},
		},
		Guardrails: dexco.Guardrails{
			ApprovalPolicy: dexco.ApprovalPolicyRequireForSensitive,
			Reviewer: func(_ context.Context, _ dexco.Turn, request dexco.ToolApprovalRequest) (dexco.ApprovalDecision, error) {
				reviewerCalls++
				if request.Guardrail.Risk != dexco.ToolRiskWorkspaceWrite {
					t.Fatalf("guardrail risk = %q, want %q", request.Guardrail.Risk, dexco.ToolRiskWorkspaceWrite)
				}
				return dexco.ApprovalDecisionApproved, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	sink := &clientEventSink{}
	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "guard"},
	}, sink)
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "approval handled" {
		t.Fatalf("FinalMessage = %q, want approval handled", result.FinalMessage)
	}
	if tool.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.calls)
	}
	if hookCalls != 1 {
		t.Fatalf("hook calls = %d, want 1", hookCalls)
	}
	if reviewerCalls != 1 {
		t.Fatalf("reviewer calls = %d, want 1", reviewerCalls)
	}
	requestIndex := clientEventIndex(sink.events, dexco.ClientEventToolApprovalRequest)
	decisionIndex := clientEventIndex(sink.events, dexco.ClientEventToolApprovalDecision)
	if requestIndex == -1 || decisionIndex == -1 {
		t.Fatalf("approval events = %#v, want request and decision", clientEventTypes(sink.events))
	}
	if requestIndex > decisionIndex {
		t.Fatalf("approval request index = %d, want before decision index %d", requestIndex, decisionIndex)
	}
	request := sink.events[requestIndex].ToolApprovalRequest
	if request == nil || request.Call.Name != "sensitive_tool" {
		t.Fatalf("approval request = %#v, want sensitive_tool request", request)
	}
	if sink.events[decisionIndex].ApprovalDecision != dexco.ApprovalDecisionApproved {
		t.Fatalf("approval decision = %q, want %q", sink.events[decisionIndex].ApprovalDecision, dexco.ApprovalDecisionApproved)
	}
}

type deniedGuardrailClient struct {
	t     *testing.T
	calls int
}

func (c *deniedGuardrailClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	if c.calls == 1 {
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "denied-call",
			Name:      "denied_tool",
			Arguments: json.RawMessage(`{}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	}
	if !containsToolResult(prompt.History, "denied-call", "tool call denied by guardrail: forbidden by handler") {
		c.t.Fatalf("prompt missing denied tool output: %#v", prompt.History)
	}
	item := dexco.AssistantMessageItem("denied handled")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

type deniedTool struct {
	calls int
}

func (t *deniedTool) Name() string {
	return "denied_tool"
}

func (t *deniedTool) Spec() dexco.ToolSpec {
	return dexco.ToolSpec{Name: "denied_tool"}
}

func (t *deniedTool) Guardrail(context.Context, dexco.ToolCall) (dexco.ToolGuardrail, error) {
	return dexco.ToolGuardrail{
		Risk:                dexco.ToolRiskDestructive,
		ApprovalRequirement: dexco.ApprovalRequirementDenied,
		Reason:              "forbidden by handler",
	}, nil
}

func (t *deniedTool) Call(context.Context, dexco.ToolCall) (dexco.ToolResult, error) {
	t.calls++
	return dexco.ToolResult{Output: "should not run", Success: true}, nil
}

// Adapted from Codex sandboxing/session guardrail tests. A handler-level Denied
// requirement is stronger than the runner's broad allow-all policy: the side
// effect must not dispatch, and the model should observe a failed tool result.
func TestGuardrailDeniedRequirementSkipsDispatchUnderAllowAll(t *testing.T) {
	t.Parallel()

	tool := &deniedTool{}
	router, err := dexco.NewRouter(tool)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &deniedGuardrailClient{t: t}
	turnRunner, err := dexco.NewRunnerWithOptions(client, router, dexco.RunnerOptions{
		Guardrails: dexco.Guardrails{
			ApprovalPolicy: dexco.ApprovalPolicyAllowAll,
		},
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	sink := &clientEventSink{}
	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "try denied tool"},
	}, sink)
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "denied handled" {
		t.Fatalf("FinalMessage = %q, want denied handled", result.FinalMessage)
	}
	if tool.calls != 0 {
		t.Fatalf("tool calls = %d, want 0", tool.calls)
	}
	if clientEventIndex(sink.events, dexco.ClientEventToolApprovalDecision) == -1 {
		t.Fatalf("client events missing approval decision: %#v", clientEventTypes(sink.events))
	}
}

type repeatedDeniedGuardrailClient struct {
	t     *testing.T
	calls int
}

func (c *repeatedDeniedGuardrailClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	if c.calls > 1 {
		previousCallID := fmt.Sprintf("denied-call-%d", c.calls-1)
		if !containsToolResult(prompt.History, previousCallID, "tool call denied by guardrail: forbidden by handler") {
			c.t.Fatalf("prompt missing previous denied tool output %q: %#v", previousCallID, prompt.History)
		}
	}
	callID := fmt.Sprintf("denied-call-%d", c.calls)
	item := dexco.ToolCallItem(dexco.ToolCall{
		CallID:    callID,
		Name:      "denied_tool",
		Arguments: json.RawMessage(`{}`),
	})
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

// Adapted from Codex Guardian rejection circuit-breaker tests. Rejections are
// normally model-visible failed tool results, but a model that repeatedly asks
// for denied actions in the same turn should not be allowed to spin forever.
func TestGuardrailRepeatedDenialsInterruptTurn(t *testing.T) {
	t.Parallel()

	tool := &deniedTool{}
	router, err := dexco.NewRouter(tool)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &repeatedDeniedGuardrailClient{t: t}
	turnRunner, err := dexco.NewRunnerWithOptions(client, router, dexco.RunnerOptions{
		Guardrails: dexco.Guardrails{
			ApprovalPolicy: dexco.ApprovalPolicyAllowAll,
		},
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "keep asking for denied tools"},
	}, dexco.NopSink{})
	if err == nil || !strings.Contains(err.Error(), "guardrail denied too many tool calls") {
		t.Fatalf("SubmitUserInput() error = %v, want guardrail circuit breaker", err)
	}
	if client.calls != 3 {
		t.Fatalf("model calls = %d, want 3 denial attempts before interruption", client.calls)
	}
	if tool.calls != 0 {
		t.Fatalf("tool calls = %d, want 0", tool.calls)
	}
}

type grantAwareClient struct {
	t     *testing.T
	calls int
}

func (c *grantAwareClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	switch c.calls {
	case 1:
		item := requestPermissionsItem("permissions-call")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 2:
		if !containsToolResultContaining(prompt.History, "permissions-call", permissionGrantKey) {
			c.t.Fatalf("prompt missing request_permissions grant output: %#v", prompt.History)
		}
		item := grantSensitiveToolItem("granted-call")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 3:
		if !containsToolResult(prompt.History, "granted-call", "grant-sensitive-output") {
			c.t.Fatalf("prompt missing granted tool output: %#v", prompt.History)
		}
		item := dexco.AssistantMessageItem("permission grant handled")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", c.calls)
		return nil, nil
	}
}

type strictGrantAwareClient struct {
	t     *testing.T
	calls int
}

func (c *strictGrantAwareClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	switch c.calls {
	case 1:
		item := requestPermissionsItem("permissions-call")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 2:
		if !containsToolResultContaining(prompt.History, "permissions-call", permissionGrantKey) {
			c.t.Fatalf("prompt missing strict request_permissions grant output: %#v", prompt.History)
		}
		item := grantSensitiveToolItem("strict-granted-call")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 3:
		if !containsToolResult(prompt.History, "strict-granted-call", "grant-sensitive-output") {
			c.t.Fatalf("prompt missing strict approved tool output: %#v", prompt.History)
		}
		item := dexco.AssistantMessageItem("strict permission grant handled")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", c.calls)
		return nil, nil
	}
}

type grantAcrossTurnsClient struct {
	t         *testing.T
	calls     int
	wantReuse bool
}

func (c *grantAcrossTurnsClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	switch c.calls {
	case 1:
		item := requestPermissionsItem("permissions-call")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 2:
		if !containsToolResultContaining(prompt.History, "permissions-call", permissionGrantKey) {
			c.t.Fatalf("first turn prompt missing request_permissions grant output: %#v", prompt.History)
		}
		item := dexco.AssistantMessageItem("grant recorded")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 3:
		item := grantSensitiveToolItem("later-call")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 4:
		if c.wantReuse {
			if !containsToolResult(prompt.History, "later-call", "grant-sensitive-output") {
				c.t.Fatalf("second turn prompt missing preapproved tool output: %#v", prompt.History)
			}
		} else if !containsToolResult(prompt.History, "later-call", "tool call denied by guardrail: reviewer denied tool call") {
			c.t.Fatalf("second turn prompt missing denied tool output: %#v", prompt.History)
		}
		item := dexco.AssistantMessageItem("second turn handled")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", c.calls)
		return nil, nil
	}
}

type partialGrantClient struct {
	t     *testing.T
	calls int
}

func (c *partialGrantClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	switch c.calls {
	case 1:
		item := requestPermissionsItemWithGrants("permissions-call", []dexco.PermissionGrant{
			{
				Key:         permissionGrantKey,
				Description: "write generated reports",
			},
			{
				Key:         secondPermissionGrantKey,
				Description: "write private reports",
			},
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 2:
		if !containsToolResultContaining(prompt.History, "permissions-call", permissionGrantKey) {
			c.t.Fatalf("prompt missing granted permission key: %#v", prompt.History)
		}
		if containsToolResultContaining(prompt.History, "permissions-call", secondPermissionGrantKey) {
			c.t.Fatalf("prompt included ungranted permission key in tool result: %#v", prompt.History)
		}
		item := grantSensitiveToolItem("partial-call")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 3:
		if !containsToolResult(prompt.History, "partial-call", "tool call denied by guardrail: reviewer denied tool call") {
			c.t.Fatalf("prompt missing denied partial-grant tool output: %#v", prompt.History)
		}
		item := dexco.AssistantMessageItem("partial grant handled")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", c.calls)
		return nil, nil
	}
}

const permissionGrantKey = "workspace-write:reports"
const secondPermissionGrantKey = "workspace-write:private"

func requestPermissionsItem(callID string) dexco.Item {
	return requestPermissionsItemWithGrants(callID, []dexco.PermissionGrant{{
		Key:         permissionGrantKey,
		Description: "write generated reports",
	}})
}

func requestPermissionsItemWithGrants(callID string, grants []dexco.PermissionGrant) dexco.Item {
	args, _ := json.Marshal(dexco.RequestPermissionsArgs{
		Reason: "Allow writing generated reports",
		Grants: grants,
	})
	return dexco.ToolCallItem(dexco.ToolCall{
		CallID:    callID,
		Name:      "request_permissions",
		Arguments: args,
	})
}

func grantSensitiveToolItem(callID string) dexco.Item {
	return dexco.ToolCallItem(dexco.ToolCall{
		CallID:    callID,
		Name:      "grant_sensitive_tool",
		Arguments: json.RawMessage(`{}`),
	})
}

type grantSensitiveTool struct {
	calls int
	key   string
}

func (t *grantSensitiveTool) Name() string {
	return "grant_sensitive_tool"
}

func (t *grantSensitiveTool) Spec() dexco.ToolSpec {
	return dexco.ToolSpec{Name: "grant_sensitive_tool"}
}

func (t *grantSensitiveTool) Guardrail(context.Context, dexco.ToolCall) (dexco.ToolGuardrail, error) {
	key := t.key
	if key == "" {
		key = permissionGrantKey
	}
	return dexco.ToolGuardrail{
		Risk:                dexco.ToolRiskWorkspaceWrite,
		ApprovalRequirement: dexco.ApprovalRequirementRequired,
		Reason:              "write generated reports",
		PermissionGrantKey:  key,
	}, nil
}

func (t *grantSensitiveTool) Call(context.Context, dexco.ToolCall) (dexco.ToolResult, error) {
	t.calls++
	return dexco.ToolResult{Output: "grant-sensitive-output", Success: true}, nil
}

// Adapted from Codex request_permissions sticky-turn tests. Codex records a
// granted additional-permission profile in turn state so later shell-like tools
// do not ask for the same approval again. Dexco keeps the same library
// invariant with opaque guardrail grant keys instead of OS sandbox profiles.
func TestRequestPermissionsTurnGrantPreapprovesLaterToolInSameTurn(t *testing.T) {
	t.Parallel()

	grants := dexco.NewPermissionGrantStore()
	tool := &grantSensitiveTool{}
	router, err := dexco.NewRouter(
		dexco.RequestPermissionsHandler{
			Grants: grants,
			Responder: func(_ context.Context, request dexco.RequestPermissionsArgs) (dexco.RequestPermissionsResponse, error) {
				return dexco.RequestPermissionsResponse{
					Grants: request.Grants,
					Scope:  dexco.PermissionGrantScopeTurn,
				}, nil
			},
		},
		tool,
	)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	reviewerCalls := 0
	turnRunner, err := dexco.NewRunnerWithOptions(
		&grantAwareClient{t: t},
		router,
		dexco.RunnerOptions{
			Guardrails: dexco.Guardrails{
				ApprovalPolicy:   dexco.ApprovalPolicyRequireForSensitive,
				PermissionGrants: grants,
				Reviewer: func(context.Context, dexco.Turn, dexco.ToolApprovalRequest) (dexco.ApprovalDecision, error) {
					reviewerCalls++
					return dexco.ApprovalDecisionDenied, nil
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "grant and use report permission"},
	}, &clientEventSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "permission grant handled" {
		t.Fatalf("FinalMessage = %q, want permission grant handled", result.FinalMessage)
	}
	if reviewerCalls != 0 {
		t.Fatalf("reviewer calls = %d, want 0 for preapproved grant", reviewerCalls)
	}
	if tool.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.calls)
	}
}

// Adapted from Codex Guardian strict-auto-review tests. A strict turn grant
// should prove that the later tool is within the requested permission key, but
// it must not bypass the reviewer. Codex uses this for Guardian assessment after
// request_permissions; Dexco preserves the same behavior with opaque grant keys.
func TestRequestPermissionsStrictTurnGrantStillRequiresReviewer(t *testing.T) {
	t.Parallel()

	grants := dexco.NewPermissionGrantStore()
	tool := &grantSensitiveTool{}
	router, err := dexco.NewRouter(
		dexco.RequestPermissionsHandler{
			Grants: grants,
			Responder: func(_ context.Context, request dexco.RequestPermissionsArgs) (dexco.RequestPermissionsResponse, error) {
				return dexco.RequestPermissionsResponse{
					Grants:           request.Grants,
					Scope:            dexco.PermissionGrantScopeTurn,
					StrictAutoReview: true,
				}, nil
			},
		},
		tool,
	)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	reviewerCalls := 0
	turnRunner, err := dexco.NewRunnerWithOptions(
		&strictGrantAwareClient{t: t},
		router,
		dexco.RunnerOptions{
			Guardrails: dexco.Guardrails{
				ApprovalPolicy:   dexco.ApprovalPolicyRequireForSensitive,
				PermissionGrants: grants,
				Reviewer: func(context.Context, dexco.Turn, dexco.ToolApprovalRequest) (dexco.ApprovalDecision, error) {
					reviewerCalls++
					return dexco.ApprovalDecisionApproved, nil
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	sink := &clientEventSink{}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "grant strictly and review report permission"},
	}, sink)
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "strict permission grant handled" {
		t.Fatalf("FinalMessage = %q, want strict permission grant handled", result.FinalMessage)
	}
	if reviewerCalls != 1 {
		t.Fatalf("reviewer calls = %d, want 1 for strict turn grant", reviewerCalls)
	}
	if tool.calls != 1 {
		t.Fatalf("tool calls = %d, want 1 after reviewer approval", tool.calls)
	}
	if !hasClientEvent(sink.events, dexco.ClientEventToolApprovalRequest) ||
		!hasClientEvent(sink.events, dexco.ClientEventToolApprovalDecision) {
		t.Fatalf("strict grant approval events missing: %#v", clientEventTypes(sink.events))
	}
}

// Adapted from Codex request_permissions partial-grant coverage. A grant for
// one requested permission must not silently approve a later tool that asks for
// a different key from the same original request.
func TestRequestPermissionsPartialGrantDoesNotPreapproveOtherKeys(t *testing.T) {
	t.Parallel()

	grants := dexco.NewPermissionGrantStore()
	tool := &grantSensitiveTool{key: secondPermissionGrantKey}
	router, err := dexco.NewRouter(
		dexco.RequestPermissionsHandler{
			Grants: grants,
			Responder: func(context.Context, dexco.RequestPermissionsArgs) (dexco.RequestPermissionsResponse, error) {
				return dexco.RequestPermissionsResponse{
					Grants: []dexco.PermissionGrant{{
						Key:         permissionGrantKey,
						Description: "write generated reports",
					}},
					Scope: dexco.PermissionGrantScopeTurn,
				}, nil
			},
		},
		tool,
	)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	reviewerCalls := 0
	turnRunner, err := dexco.NewRunnerWithOptions(&partialGrantClient{t: t}, router, dexco.RunnerOptions{
		Guardrails: dexco.Guardrails{
			ApprovalPolicy:   dexco.ApprovalPolicyRequireForSensitive,
			PermissionGrants: grants,
			Reviewer: func(context.Context, dexco.Turn, dexco.ToolApprovalRequest) (dexco.ApprovalDecision, error) {
				reviewerCalls++
				return dexco.ApprovalDecisionDenied, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "grant one permission but use another"},
	}, &clientEventSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "partial grant handled" {
		t.Fatalf("FinalMessage = %q, want partial grant handled", result.FinalMessage)
	}
	if reviewerCalls != 1 {
		t.Fatalf("reviewer calls = %d, want 1 for ungranted key", reviewerCalls)
	}
	if tool.calls != 0 {
		t.Fatalf("tool calls = %d, want 0 after reviewer denial", tool.calls)
	}
}

// Adapted from Codex request_permissions grant lifetime coverage. Turn-scoped
// grants are deliberately not durable session state; a later turn must ask the
// reviewer again even when the same guardrail key appears.
func TestRequestPermissionsTurnGrantDoesNotCarryAcrossTurns(t *testing.T) {
	t.Parallel()

	grants := dexco.NewPermissionGrantStore()
	tool := &grantSensitiveTool{}
	router, err := dexco.NewRouter(
		dexco.RequestPermissionsHandler{
			Grants: grants,
			Responder: func(_ context.Context, request dexco.RequestPermissionsArgs) (dexco.RequestPermissionsResponse, error) {
				return dexco.RequestPermissionsResponse{
					Grants: request.Grants,
					Scope:  dexco.PermissionGrantScopeTurn,
				}, nil
			},
		},
		tool,
	)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &grantAcrossTurnsClient{
		t:         t,
		wantReuse: false,
	}
	reviewerCalls := 0
	turnRunner, err := dexco.NewRunnerWithOptions(client, router, dexco.RunnerOptions{
		Guardrails: dexco.Guardrails{
			ApprovalPolicy:   dexco.ApprovalPolicyRequireForSensitive,
			PermissionGrants: grants,
			Reviewer: func(context.Context, dexco.Turn, dexco.ToolApprovalRequest) (dexco.ApprovalDecision, error) {
				reviewerCalls++
				return dexco.ApprovalDecisionDenied, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "grant for this turn"},
	}, &clientEventSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() first error = %v", err)
	}
	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "try reuse next turn"},
	}, &clientEventSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() second error = %v", err)
	}

	if result.FinalMessage != "second turn handled" {
		t.Fatalf("FinalMessage = %q, want second turn handled", result.FinalMessage)
	}
	if reviewerCalls != 1 {
		t.Fatalf("reviewer calls = %d, want 1 after turn grant expired", reviewerCalls)
	}
	if tool.calls != 0 {
		t.Fatalf("tool calls = %d, want 0 after reviewer denial", tool.calls)
	}
}

// Adapted from Codex request_permissions session-grant coverage. Session-scoped
// grants persist across turns when the embedding application chooses that
// lifetime, but Dexco still requires an exact guardrail key match.
func TestRequestPermissionsSessionGrantCarriesAcrossTurns(t *testing.T) {
	t.Parallel()

	grants := dexco.NewPermissionGrantStore()
	tool := &grantSensitiveTool{}
	router, err := dexco.NewRouter(
		dexco.RequestPermissionsHandler{
			Grants: grants,
			Responder: func(_ context.Context, request dexco.RequestPermissionsArgs) (dexco.RequestPermissionsResponse, error) {
				return dexco.RequestPermissionsResponse{
					Grants: request.Grants,
					Scope:  dexco.PermissionGrantScopeSession,
				}, nil
			},
		},
		tool,
	)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &grantAcrossTurnsClient{
		t:         t,
		wantReuse: true,
	}
	reviewerCalls := 0
	turnRunner, err := dexco.NewRunnerWithOptions(client, router, dexco.RunnerOptions{
		Guardrails: dexco.Guardrails{
			ApprovalPolicy:   dexco.ApprovalPolicyRequireForSensitive,
			PermissionGrants: grants,
			Reviewer: func(context.Context, dexco.Turn, dexco.ToolApprovalRequest) (dexco.ApprovalDecision, error) {
				reviewerCalls++
				return dexco.ApprovalDecisionDenied, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	_, err = session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "grant for session"},
	}, &clientEventSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() first error = %v", err)
	}
	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "reuse session grant"},
	}, &clientEventSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() second error = %v", err)
	}

	if result.FinalMessage != "second turn handled" {
		t.Fatalf("FinalMessage = %q, want second turn handled", result.FinalMessage)
	}
	if reviewerCalls != 0 {
		t.Fatalf("reviewer calls = %d, want 0 for session grant reuse", reviewerCalls)
	}
	if tool.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.calls)
	}
}

type execCommandGrantClient struct {
	t     *testing.T
	calls int
}

func (c *execCommandGrantClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	switch c.calls {
	case 1:
		const execCommandGrantKey = `exec_command:["printf","granted"]`
		item := requestPermissionsItemWithGrants("permissions-call", []dexco.PermissionGrant{{
			Key:         execCommandGrantKey,
			Description: "run printf granted",
		}})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 2:
		if !containsToolResultContaining(prompt.History, "permissions-call", `exec_command:[\"printf\",\"granted\"]`) {
			c.t.Fatalf("prompt missing exec_command grant output: %#v", prompt.History)
		}
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "exec-call",
			Name:      "exec_command",
			Arguments: json.RawMessage(`{"cmd":"printf   granted"}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 3:
		if !containsToolResultContaining(prompt.History, "exec-call", "Output:\ngranted") {
			c.t.Fatalf("prompt missing preapproved exec_command output: %#v", prompt.History)
		}
		item := dexco.AssistantMessageItem("exec permission grant handled")
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &item},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", c.calls)
		return nil, nil
	}
}

// Adapted from Codex shell approval-cache behavior plus request_permissions
// coverage. Codex canonicalizes command argv before matching approval grants;
// Dexco uses the exec_command guardrail's canonical PermissionGrantKey so a
// grant for `printf granted` also preapproves the same command with incidental
// shell-spacing differences.
func TestRequestPermissionsGrantPreapprovesCanonicalExecCommand(t *testing.T) {
	t.Parallel()

	grants := dexco.NewPermissionGrantStore()
	router, err := dexco.NewRouter(
		dexco.RequestPermissionsHandler{
			Grants: grants,
			Responder: func(_ context.Context, request dexco.RequestPermissionsArgs) (dexco.RequestPermissionsResponse, error) {
				return dexco.RequestPermissionsResponse{
					Grants: request.Grants,
					Scope:  dexco.PermissionGrantScopeTurn,
				}, nil
			},
		},
		dexco.ExecCommandHandler{},
	)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	reviewerCalls := 0
	turnRunner, err := dexco.NewRunnerWithOptions(&execCommandGrantClient{t: t}, router, dexco.RunnerOptions{
		Guardrails: dexco.Guardrails{
			ApprovalPolicy:   dexco.ApprovalPolicyRequireForSensitive,
			PermissionGrants: grants,
			Reviewer: func(context.Context, dexco.Turn, dexco.ToolApprovalRequest) (dexco.ApprovalDecision, error) {
				reviewerCalls++
				return dexco.ApprovalDecisionDenied, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "grant and use exec permission"},
	}, &clientEventSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "exec permission grant handled" {
		t.Fatalf("FinalMessage = %q, want exec permission grant handled", result.FinalMessage)
	}
	if reviewerCalls != 0 {
		t.Fatalf("reviewer calls = %d, want 0 for preapproved exec command", reviewerCalls)
	}
}

// Adapted from Codex core's tool_results_grouped assertion. The model-visible
// history should contain all tool-call items first, followed by tool outputs in
// matching call order, even if execution can be parallelized internally.
type groupedToolClient struct {
	t     *testing.T
	calls int
}

func (c *groupedToolClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	if c.calls == 1 {
		first := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "call-1",
			Name:      "ordered_tool",
			Arguments: json.RawMessage(`{}`),
		})
		second := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "call-2",
			Name:      "ordered_tool",
			Arguments: json.RawMessage(`{}`),
		})
		third := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "call-3",
			Name:      "ordered_tool",
			Arguments: json.RawMessage(`{}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &first},
				{Type: dexco.EventOutputItemDone, Item: &second},
				{Type: dexco.EventOutputItemDone, Item: &third},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	}
	assertAllToolResultsAfterCalls(c.t, prompt.History, "call-1", "call-2", "call-3")
	item := dexco.AssistantMessageItem("grouped done")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

type orderedTool struct{}

func (orderedTool) Name() string {
	return "ordered_tool"
}

func (orderedTool) Spec() dexco.ToolSpec {
	return dexco.ToolSpec{Name: "ordered_tool"}
}

func (orderedTool) Call(_ context.Context, call dexco.ToolCall) (dexco.ToolResult, error) {
	return dexco.ToolResult{
		Output:  call.CallID,
		Success: true,
	}, nil
}

func TestRunnerGroupsToolResultsAfterAllToolCalls(t *testing.T) {
	t.Parallel()

	router, err := dexco.NewRouter(orderedTool{})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(&groupedToolClient{t: t}, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input: dexco.UserInput{Content: "group tools"},
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() error = %v", err)
	}

	if result.FinalMessage != "grouped done" {
		t.Fatalf("FinalMessage = %q, want grouped done", result.FinalMessage)
	}
}

type parallelClient struct {
	calls int
}

func (c *parallelClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	if c.calls == 1 {
		first := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "call-1",
			Name:      "parallel_tool",
			Arguments: json.RawMessage(`{}`),
		})
		second := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "call-2",
			Name:      "parallel_tool",
			Arguments: json.RawMessage(`{}`),
		})
		return &sliceStream{
			events: []dexco.ResponseEvent{
				{Type: dexco.EventOutputItemDone, Item: &first},
				{Type: dexco.EventOutputItemDone, Item: &second},
				{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	}
	if !containsToolResult(prompt.History, "call-1", "call-1") ||
		!containsToolResult(prompt.History, "call-2", "call-2") {
		return nil, errors.New("second prompt missing parallel tool outputs")
	}
	item := dexco.AssistantMessageItem("parallel done")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

type blockingParallelTool struct {
	started chan string
	release <-chan struct{}
}

func (blockingParallelTool) Name() string {
	return "parallel_tool"
}

func (blockingParallelTool) Spec() dexco.ToolSpec {
	return dexco.ToolSpec{Name: "parallel_tool"}
}

func (blockingParallelTool) SupportsParallel() bool {
	return true
}

func (t blockingParallelTool) Call(_ context.Context, call dexco.ToolCall) (dexco.ToolResult, error) {
	t.started <- call.CallID
	<-t.release
	return dexco.ToolResult{
		Output:  call.CallID,
		Success: true,
	}, nil
}

func TestRunnerCanDispatchParallelSafeToolsConcurrently(t *testing.T) {
	t.Parallel()

	started := make(chan string, 2)
	release := make(chan struct{})
	router, err := dexco.NewRouter(blockingParallelTool{
		started: started,
		release: release,
	})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunnerWithOptions(&parallelClient{}, router, dexco.RunnerOptions{
		ParallelTools: true,
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	type resultErr struct {
		result dexco.TurnResult
		err    error
	}
	done := make(chan resultErr, 1)
	go func() {
		result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: "parallel"},
		}, dexco.NopSink{})
		done <- resultErr{result: result, err: err}
	}()

	waitForStartedCalls(t, started, release, "call-1", "call-2")
	close(release)

	outcome := <-done
	if outcome.err != nil {
		t.Fatalf("SubmitUserInput() error = %v", outcome.err)
	}
	if outcome.result.FinalMessage != "parallel done" {
		t.Fatalf("FinalMessage = %q, want parallel done", outcome.result.FinalMessage)
	}
}

// Adapted from Codex core's
// `shell_tools_start_before_response_completed_when_stream_delayed`. Once the
// model has emitted a completed tool-call item, Codex starts the tool while the
// response stream is still open; it only waits to send tool outputs back to the
// model until after response.completed makes the sampling attempt durable.
type delayedCompletionClient struct {
	t          *testing.T
	completion <-chan struct{}
	calls      int
}

func (c *delayedCompletionClient) Stream(_ context.Context, prompt dexco.Prompt) (dexco.EventStream, error) {
	c.calls++
	if c.calls == 1 {
		return &delayedCompletionStream{completion: c.completion}, nil
	}
	if !containsToolResult(prompt.History, "early-call", "early-call") {
		c.t.Fatalf("second prompt missing early tool result history: %#v", prompt.History)
	}
	item := dexco.AssistantMessageItem("early dispatch done")
	return &sliceStream{
		events: []dexco.ResponseEvent{
			{Type: dexco.EventOutputItemDone, Item: &item},
			{Type: dexco.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

type delayedCompletionStream struct {
	completion <-chan struct{}
	index      int
}

func (s *delayedCompletionStream) Recv() (dexco.ResponseEvent, error) {
	switch s.index {
	case 0:
		s.index++
		item := dexco.ToolCallItem(dexco.ToolCall{
			CallID:    "early-call",
			Name:      "early_tool",
			Arguments: json.RawMessage(`{}`),
		})
		return dexco.ResponseEvent{Type: dexco.EventOutputItemDone, Item: &item}, nil
	case 1:
		s.index++
		<-s.completion
		return dexco.ResponseEvent{Type: dexco.EventCompleted, EndTurn: boolPtr(true)}, nil
	default:
		return dexco.ResponseEvent{}, io.EOF
	}
}

type earlyDispatchTool struct {
	started chan<- string
	release <-chan struct{}
}

func (earlyDispatchTool) Name() string {
	return "early_tool"
}

func (earlyDispatchTool) Spec() dexco.ToolSpec {
	return dexco.ToolSpec{Name: "early_tool"}
}

func (t earlyDispatchTool) Call(_ context.Context, call dexco.ToolCall) (dexco.ToolResult, error) {
	t.started <- call.CallID
	<-t.release
	return dexco.ToolResult{
		Output:  call.CallID,
		Success: true,
	}, nil
}

func TestRunnerStartsToolsBeforeResponseCompleted(t *testing.T) {
	t.Parallel()

	started := make(chan string, 1)
	release := make(chan struct{})
	completion := make(chan struct{})
	router, err := dexco.NewRouter(earlyDispatchTool{
		started: started,
		release: release,
	})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(&delayedCompletionClient{
		t:          t,
		completion: completion,
	}, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	type resultErr struct {
		result dexco.TurnResult
		err    error
	}
	done := make(chan resultErr, 1)
	go func() {
		result, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
			Input: dexco.UserInput{Content: "start early"},
		}, dexco.NopSink{})
		done <- resultErr{result: result, err: err}
	}()

	select {
	case callID := <-started:
		if callID != "early-call" {
			close(release)
			close(completion)
			t.Fatalf("started call = %q, want early-call", callID)
		}
	case <-time.After(time.Second):
		close(release)
		close(completion)
		t.Fatalf("tool did not start before response.completed")
	}
	select {
	case outcome := <-done:
		close(release)
		close(completion)
		t.Fatalf("turn completed before response.completed: result=%#v err=%v", outcome.result, outcome.err)
	default:
	}

	close(release)
	select {
	case outcome := <-done:
		close(completion)
		t.Fatalf("turn completed before response.completed: result=%#v err=%v", outcome.result, outcome.err)
	default:
	}
	close(completion)

	outcome := <-done
	if outcome.err != nil {
		t.Fatalf("SubmitUserInput() error = %v", outcome.err)
	}
	if outcome.result.FinalMessage != "early dispatch done" {
		t.Fatalf("FinalMessage = %q, want early dispatch done", outcome.result.FinalMessage)
	}
}

func waitForStartedCalls(
	t *testing.T,
	started <-chan string,
	release chan<- struct{},
	want ...string,
) {
	t.Helper()
	got := make([]string, 0, len(want))
	for len(got) < len(want) {
		select {
		case callID := <-started:
			got = append(got, callID)
		case <-time.After(time.Second):
			close(release)
			t.Fatalf("started calls = %#v, want %#v", got, want)
		}
	}
	if !sameStringSet(got, want) {
		close(release)
		t.Fatalf("started calls = %#v, want %#v", got, want)
	}
}

func waitForClosed(t *testing.T, channel <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func assertStillOpen(t *testing.T, channel <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-channel:
		t.Fatal(failure)
	default:
	}
}

func assertJSONRawEqual(t *testing.T, got json.RawMessage, want json.RawMessage, description string) {
	t.Helper()
	var gotJSON any
	if err := json.Unmarshal(got, &gotJSON); err != nil {
		t.Fatalf("%s got value is not JSON: %v; raw=%s", description, err, string(got))
	}
	var wantJSON any
	if err := json.Unmarshal(want, &wantJSON); err != nil {
		t.Fatalf("%s want value is not JSON: %v; raw=%s", description, err, string(want))
	}
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Fatalf("%s = %#v, want %#v", description, gotJSON, wantJSON)
	}
}

func assertStringSliceEqual(t *testing.T, got []string, want []string, description string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v", description, got, want)
	}
}

func sameStringSet(a []string, b []string) bool {
	counts := make(map[string]int, len(a))
	for _, value := range a {
		counts[value]++
	}
	for _, value := range b {
		counts[value]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func eventTypes(events []dexco.ResponseEvent) []dexco.ResponseEventType {
	types := make([]dexco.ResponseEventType, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func promptToolNames(specs []dexco.ToolSpec) []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return names
}

func newContextTestSession(t *testing.T, client dexco.ModelClient) *dexco.Session {
	t.Helper()
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	return session
}

func newPermissionInstructionsTestSession(
	t *testing.T,
	client dexco.ModelClient,
	instructions dexco.PermissionInstructionsConfig,
) *dexco.Session {
	t.Helper()
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{
		PermissionInstructions: instructions,
	}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	return session
}

func newModelSwitchInstructionsTestSession(
	t *testing.T,
	client dexco.ModelClient,
	instructions dexco.ModelSwitchInstructionsConfig,
) *dexco.Session {
	t.Helper()
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{
		ModelSwitchInstructions: instructions,
	}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	return session
}

func newCollaborationInstructionsTestSession(
	t *testing.T,
	client dexco.ModelClient,
	instructions dexco.CollaborationInstructionsConfig,
) *dexco.Session {
	t.Helper()
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{
		CollaborationInstructions: instructions,
	}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	return session
}

func newStyleInstructionsTestSession(
	t *testing.T,
	client dexco.ModelClient,
	instructions dexco.StyleInstructionsConfig,
) *dexco.Session {
	t.Helper()
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunner(client, router)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{
		StyleInstructions: instructions,
	}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	return session
}

func collaborationInstructions(text string) string {
	return "<collaboration_mode>" + text + "</collaboration_mode>"
}

func modelSwitchInstructions(text string) string {
	return "<model_switch>\nThe user was previously using a different model. Please continue the conversation according to the following instructions:\n\n" + text + "\n</model_switch>"
}

func styleInstructions(text string) string {
	return "<personality_spec> The user has requested a new communication style. Future messages should adhere to the following personality: \n" + text + " </personality_spec>"
}

func itemLayout(history []dexco.Item) []string {
	layout := make([]string, 0, len(history))
	for _, item := range history {
		switch item.Kind {
		case dexco.ItemContext:
			layout = append(layout, fmt.Sprintf("context:%s:%s", item.Role, item.Content))
		case dexco.ItemUserInput:
			layout = append(layout, "user:"+item.Content)
		case dexco.ItemAssistantMessage:
			layout = append(layout, "assistant:"+item.Content)
		case dexco.ItemReasoning:
			layout = append(layout, "reasoning:"+item.Content)
		case dexco.ItemToolCall:
			if item.ToolCall != nil {
				layout = append(layout, "tool_call:"+item.ToolCall.CallID)
			}
		case dexco.ItemToolResult:
			if item.ToolResult != nil {
				layout = append(layout, "tool_result:"+item.ToolResult.CallID)
			}
		case dexco.ItemWebSearch:
			layout = append(layout, "web_search:"+item.Content)
		case dexco.ItemHookPrompt:
			layout = append(layout, "hook_prompt:"+item.Content)
		case dexco.ItemImageGeneration:
			if item.ImageGeneration != nil {
				layout = append(layout, "image_generation:"+item.ImageGeneration.ID)
			}
		}
	}
	return layout
}

func historyContainsContent(history []dexco.Item, content string) bool {
	for _, item := range history {
		if item.Content == content {
			return true
		}
		for _, part := range item.Parts {
			if part.Text == content {
				return true
			}
		}
		if item.ToolResult != nil && item.ToolResult.Output == content {
			return true
		}
	}
	return false
}

func newRetryPolicyTestSession(t *testing.T, client dexco.ModelClient) *dexco.Session {
	t.Helper()
	router, err := dexco.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	turnRunner, err := dexco.NewRunnerWithOptions(client, router, dexco.RunnerOptions{
		MaxModelRetries: 2,
	})
	if err != nil {
		t.Fatalf("NewRunnerWithOptions() error = %v", err)
	}
	session, err := dexco.NewSession(dexco.Config{}, turnRunner)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	return session
}

func submitContextTurn(
	t *testing.T,
	session *dexco.Session,
	content string,
	additionalContext map[string]dexco.AdditionalContextEntry,
) {
	t.Helper()
	_, err := session.SubmitUserInput(context.Background(), dexco.OpUserInput{
		Input:             dexco.UserInput{Content: content},
		AdditionalContext: additionalContext,
	}, dexco.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput(%q) error = %v", content, err)
	}
}

func contextContents(history []dexco.Item, role string) []string {
	contents := make([]string, 0)
	for _, item := range history {
		if item.Kind == dexco.ItemContext && item.Role == role {
			contents = append(contents, item.Content)
		}
	}
	return contents
}

func allContextContents(history []dexco.Item) []string {
	contents := make([]string, 0)
	for _, item := range history {
		if item.Kind == dexco.ItemContext {
			contents = append(contents, item.Content)
		}
	}
	return contents
}

func assertTruncatedContext(t *testing.T, got string, wantPrefix string, wantSuffix string) {
	t.Helper()
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("context prefix = %q, want prefix %q", got[:min(len(got), len(wantPrefix))], wantPrefix)
	}
	if !strings.Contains(got, "tokens truncated") {
		t.Fatalf("context = %q, want truncation marker", got)
	}
	if !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("context suffix = %q, want suffix %q", got[max(0, len(got)-len(wantSuffix)):], wantSuffix)
	}
	if len(got) > 5*1024 {
		t.Fatalf("context length = %d, want <= 5120", len(got))
	}
}

func clientEventTypes(events []dexco.ClientEvent) []dexco.ClientEventType {
	types := make([]dexco.ClientEventType, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func webSearchEvents(events []dexco.ClientEvent) []dexco.Item {
	items := make([]dexco.Item, 0)
	for _, event := range events {
		if event.Type == dexco.ClientEventWebSearch && event.WebSearch != nil {
			search := *event.WebSearch
			search.Action.Queries = append([]string(nil), search.Action.Queries...)
			items = append(items, dexco.Item{
				Kind:      dexco.ItemWebSearch,
				WebSearch: &search,
			})
		}
	}
	return items
}

func hookPromptEvents(events []dexco.ClientEvent) []dexco.Item {
	items := make([]dexco.Item, 0)
	for _, event := range events {
		if event.Type == dexco.ClientEventHookPrompt && event.HookPrompt != nil {
			prompt := *event.HookPrompt
			prompt.Fragments = append([]dexco.HookPromptFragment(nil), prompt.Fragments...)
			items = append(items, dexco.Item{
				Kind:       dexco.ItemHookPrompt,
				HookPrompt: &prompt,
			})
		}
	}
	return items
}

func imageGenerationEvents(events []dexco.ClientEvent) []dexco.Item {
	items := make([]dexco.Item, 0)
	for _, event := range events {
		if event.Type == dexco.ClientEventImageGeneration && event.ImageGeneration != nil {
			imageGeneration := *event.ImageGeneration
			items = append(items, dexco.Item{
				Kind:            dexco.ItemImageGeneration,
				ImageGeneration: &imageGeneration,
			})
		}
	}
	return items
}

func hasClientEvent(events []dexco.ClientEvent, eventType dexco.ClientEventType) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func clientEventIndex(events []dexco.ClientEvent, eventType dexco.ClientEventType) int {
	for index, event := range events {
		if event.Type == eventType {
			return index
		}
	}
	return -1
}

func containsUserInput(history []dexco.Item, content string) bool {
	for _, item := range history {
		if item.Kind == dexco.ItemUserInput && item.Content == content {
			return true
		}
	}
	return false
}

func userInputContents(history []dexco.Item) []string {
	contents := make([]string, 0)
	for _, item := range history {
		if item.Kind == dexco.ItemUserInput {
			contents = append(contents, item.Content)
		}
	}
	return contents
}

func containsToolResult(history []dexco.Item, callID string, output string) bool {
	for _, item := range history {
		if item.ToolResult != nil && item.ToolResult.CallID == callID && item.ToolResult.Output == output {
			return true
		}
	}
	return false
}

func containsToolResultContaining(history []dexco.Item, callID string, output string) bool {
	for _, item := range history {
		if item.ToolResult != nil && item.ToolResult.CallID == callID && strings.Contains(item.ToolResult.Output, output) {
			return true
		}
	}
	return false
}

func containsToolCall(history []dexco.Item, callID string) bool {
	for _, item := range history {
		if item.ToolCall != nil && item.ToolCall.CallID == callID {
			return true
		}
	}
	return false
}

func containsReasoning(history []dexco.Item, content string) bool {
	for _, item := range history {
		if item.Kind == dexco.ItemReasoning && item.Content == content {
			return true
		}
	}
	return false
}

func containsAssistantMessage(history []dexco.Item, content string) bool {
	for _, item := range history {
		if item.Kind == dexco.ItemAssistantMessage && item.Content == content {
			return true
		}
	}
	return false
}

func hasToolResultPlanUpdate(history []dexco.Item, callID string) bool {
	for _, item := range history {
		if item.ToolResult != nil && item.ToolResult.CallID == callID && item.ToolResult.PlanUpdate != nil {
			return true
		}
	}
	return false
}

func containsImageToolResult(history []dexco.Item, callID string) bool {
	for _, item := range history {
		if item.ToolResult == nil || item.ToolResult.CallID != callID {
			continue
		}
		if len(item.ToolResult.Parts) != 1 {
			return false
		}
		part := item.ToolResult.Parts[0]
		return part.Kind == dexco.ContentPartImage &&
			part.Detail == "high" &&
			strings.HasPrefix(part.ImageURL, "data:image/png;base64,")
	}
	return false
}

func writeTinyPNG(t *testing.T, path string) {
	t.Helper()
	const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	data, err := base64.StdEncoding.DecodeString(tinyPNG)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func writeSizedPNG(t *testing.T, size image.Point) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "example.png")
	img := image.NewRGBA(image.Rect(0, 0, size.X, size.Y))
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	return path
}

func decodeDexcoImageBounds(t *testing.T, part dexco.ContentPart) image.Point {
	t.Helper()
	encoded := strings.TrimPrefix(part.ImageURL, "data:image/png;base64,")
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	bounds := decoded.Bounds()
	return image.Pt(bounds.Dx(), bounds.Dy())
}

func assertAllToolResultsAfterCalls(t *testing.T, history []dexco.Item, callIDs ...string) {
	t.Helper()
	lastCallIndex := -1
	resultIndexes := make([]int, 0, len(callIDs))
	for _, callID := range callIDs {
		callIndex := indexToolCall(history, callID)
		if callIndex == -1 {
			t.Fatalf("history missing tool call %q: %#v", callID, history)
		}
		if callIndex <= lastCallIndex {
			t.Fatalf("tool call %q index = %d, want after previous call index %d", callID, callIndex, lastCallIndex)
		}
		lastCallIndex = callIndex

		resultIndex := indexToolResult(history, callID)
		if resultIndex == -1 {
			t.Fatalf("history missing tool result %q: %#v", callID, history)
		}
		resultIndexes = append(resultIndexes, resultIndex)
	}
	for index, resultIndex := range resultIndexes {
		if resultIndex <= lastCallIndex {
			t.Fatalf("tool result %q index = %d, want after final tool call index %d", callIDs[index], resultIndex, lastCallIndex)
		}
		if index > 0 && resultIndex <= resultIndexes[index-1] {
			t.Fatalf("tool result %q index = %d, want after previous result index %d", callIDs[index], resultIndex, resultIndexes[index-1])
		}
	}
}

func indexToolCall(history []dexco.Item, callID string) int {
	for index, item := range history {
		if item.ToolCall != nil && item.ToolCall.CallID == callID {
			return index
		}
	}
	return -1
}

func indexToolResult(history []dexco.Item, callID string) int {
	for index, item := range history {
		if item.ToolResult != nil && item.ToolResult.CallID == callID {
			return index
		}
	}
	return -1
}

func boolPtr(value bool) *bool {
	return &value
}
