package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/openai/codex/dexco/internal/events"
	"github.com/openai/codex/dexco/internal/model"
	"github.com/openai/codex/dexco/internal/runner"
	"github.com/openai/codex/dexco/internal/tools"
)

type testModelClient struct {
	callCount int
}

func (c *testModelClient) Stream(context.Context, model.Prompt) (runner.EventStream, error) {
	c.callCount++
	if c.callCount == 1 {
		return &testStream{
			events: []model.ResponseEvent{
				{
					Type: model.EventOutputItemDone,
					Item: &model.Item{
						Kind: model.ItemToolCall,
						ToolCall: &model.ToolCall{
							CallID:    "call-1",
							Name:      "exec_command",
							Arguments: json.RawMessage(`{"cmd":"pwd"}`),
						},
					},
				},
				{Type: model.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	}

	return &testStream{
		events: []model.ResponseEvent{
			{
				Type: model.EventOutputItemDone,
				Item: &model.Item{
					Kind:    model.ItemAssistantMessage,
					Role:    "assistant",
					Content: "done",
				},
			},
			{Type: model.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

type testStream struct {
	events []model.ResponseEvent
	index  int
}

func (s *testStream) Recv() (model.ResponseEvent, error) {
	if s.index >= len(s.events) {
		return model.ResponseEvent{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

type testTool struct{}

func (testTool) Name() string { return "exec_command" }

func (testTool) Spec() model.ToolSpec { return model.ToolSpec{Name: "exec_command"} }

func (testTool) Call(context.Context, model.ToolCall) (model.ToolResult, error) {
	return model.ToolResult{
		Output:  "/tmp/worktree",
		Success: true,
	}, nil
}

func TestSessionSubmitUserInputPersistsHistoryAcrossTurns(t *testing.T) {
	t.Parallel()

	router, err := tools.NewRouter(testTool{})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	loopRunner, err := runner.New(&testModelClient{}, router)
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	sess, err := New(Config{Instructions: "Be concise."}, loopRunner)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	first, err := sess.SubmitUserInput(context.Background(), model.OpUserInput{
		Input: model.UserInput{Content: "where am i?"},
	}, events.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() first error = %v", err)
	}
	if first.FinalMessage != "done" {
		t.Fatalf("first FinalMessage = %q, want %q", first.FinalMessage, "done")
	}

	second, err := sess.SubmitUserInput(context.Background(), model.OpUserInput{
		Input: model.UserInput{Content: "and now?"},
	}, events.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() second error = %v", err)
	}

	if len(second.History) <= len(first.History) {
		t.Fatalf("second history len = %d, want greater than first len %d", len(second.History), len(first.History))
	}
	if second.TurnID == first.TurnID {
		t.Fatalf("turn ids should differ, both were %q", second.TurnID)
	}
}

// Adapted from Codex core's stream_error_allows_next_turn scenario. A failed
// turn must release the session and must not commit its user input into history,
// otherwise the next successful turn would inherit stale context.
type failThenRecoverModelClient struct {
	t       *testing.T
	prompts []model.Prompt
	calls   int
}

func (c *failThenRecoverModelClient) Stream(_ context.Context, prompt model.Prompt) (runner.EventStream, error) {
	c.prompts = append(c.prompts, prompt)
	c.calls++
	if c.calls == 1 {
		return nil, errors.New("synthetic model error")
	}
	if containsUserInput(prompt.History, "first message") {
		c.t.Fatalf("successful turn prompt included failed turn input: %#v", prompt.History)
	}
	return &testStream{
		events: []model.ResponseEvent{
			{
				Type: model.EventOutputItemDone,
				Item: &model.Item{
					Kind:    model.ItemAssistantMessage,
					Role:    "assistant",
					Content: "recovered",
				},
			},
			{Type: model.EventCompleted, EndTurn: boolPtr(true)},
		},
	}, nil
}

func TestSessionCanContinueAfterFailedTurnWithoutPersistingFailedInput(t *testing.T) {
	t.Parallel()

	router, err := tools.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &failThenRecoverModelClient{t: t}
	loopRunner, err := runner.New(client, router)
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	sess, err := New(Config{Instructions: "Be concise."}, loopRunner)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = sess.SubmitUserInput(context.Background(), model.OpUserInput{
		Input: model.UserInput{Content: "first message"},
	}, events.NopSink{})
	if err == nil {
		t.Fatalf("SubmitUserInput() first error = nil, want synthetic model error")
	}

	second, err := sess.SubmitUserInput(context.Background(), model.OpUserInput{
		Input: model.UserInput{Content: "follow up"},
	}, events.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() second error = %v", err)
	}

	if second.FinalMessage != "recovered" {
		t.Fatalf("second FinalMessage = %q, want recovered", second.FinalMessage)
	}
	if containsUserInput(second.History, "first message") {
		t.Fatalf("successful history includes failed turn input: %#v", second.History)
	}
	if !containsUserInput(second.History, "follow up") {
		t.Fatalf("successful history missing second turn input: %#v", second.History)
	}
}

// Adapted from Codex core's abort_tasks history coverage. Canceling a turn
// after a tool has started is not the same as an ordinary failed model turn:
// Codex keeps enough transcript for the next request to know that a side effect
// was attempted and may have partially executed.
func TestSessionPersistsAbortedToolTranscriptForNextTurn(t *testing.T) {
	t.Parallel()

	tool := &sessionCancelingTool{started: make(chan struct{})}
	router, err := tools.NewRouter(tool)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	client := &abortThenContinueModelClient{t: t}
	loopRunner, err := runner.New(client, router)
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	sess, err := New(Config{Instructions: "Be concise."}, loopRunner)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := sess.SubmitUserInput(ctx, model.OpUserInput{
			Input: model.UserInput{Content: "start abort"},
		}, events.NopSink{})
		done <- err
	}()

	select {
	case <-tool.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tool to start")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("SubmitUserInput() first error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for aborted turn")
	}

	second, err := sess.SubmitUserInput(context.Background(), model.OpUserInput{
		Input: model.UserInput{Content: "after abort"},
	}, events.NopSink{})
	if err != nil {
		t.Fatalf("SubmitUserInput() second error = %v", err)
	}
	if second.FinalMessage != "continued after abort" {
		t.Fatalf("second FinalMessage = %q, want continued after abort", second.FinalMessage)
	}
	if client.calls != 2 {
		t.Fatalf("model calls = %d, want 2", client.calls)
	}
}

type abortThenContinueModelClient struct {
	t     *testing.T
	calls int
}

func (c *abortThenContinueModelClient) Stream(_ context.Context, prompt model.Prompt) (runner.EventStream, error) {
	c.calls++
	switch c.calls {
	case 1:
		call := model.ToolCallItem(model.ToolCall{
			CallID:    "abort-call",
			Name:      "session_canceling_tool",
			Arguments: json.RawMessage(`{}`),
		})
		return &testStream{
			events: []model.ResponseEvent{
				{Type: model.EventOutputItemDone, Item: &call},
				{Type: model.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	case 2:
		if !containsUserInput(prompt.History, "start abort") {
			c.t.Fatalf("follow-up prompt missing aborted turn input: %#v", prompt.History)
		}
		if !containsToolCall(prompt.History, "abort-call") {
			c.t.Fatalf("follow-up prompt missing aborted tool call: %#v", prompt.History)
		}
		if !containsToolResultOutputContaining(prompt.History, "abort-call", "aborted") {
			c.t.Fatalf("follow-up prompt missing aborted tool output: %#v", prompt.History)
		}
		if !containsContextContaining(prompt.History, "<turn_aborted>") {
			c.t.Fatalf("follow-up prompt missing turn-aborted marker: %#v", prompt.History)
		}
		if !containsUserInput(prompt.History, "after abort") {
			c.t.Fatalf("follow-up prompt missing new user input: %#v", prompt.History)
		}
		message := model.AssistantMessageItem("continued after abort")
		return &testStream{
			events: []model.ResponseEvent{
				{Type: model.EventOutputItemDone, Item: &message},
				{Type: model.EventCompleted, EndTurn: boolPtr(true)},
			},
		}, nil
	default:
		c.t.Fatalf("unexpected model call %d", c.calls)
		return nil, nil
	}
}

type sessionCancelingTool struct {
	started chan struct{}
}

func (t *sessionCancelingTool) Name() string {
	return "session_canceling_tool"
}

func (t *sessionCancelingTool) Spec() model.ToolSpec {
	return model.ToolSpec{Name: "session_canceling_tool"}
}

func (t *sessionCancelingTool) Call(ctx context.Context, _ model.ToolCall) (model.ToolResult, error) {
	close(t.started)
	<-ctx.Done()
	return model.ToolResult{}, ctx.Err()
}

func containsUserInput(history []model.Item, content string) bool {
	for _, item := range history {
		if item.Kind == model.ItemUserInput && item.Content == content {
			return true
		}
	}
	return false
}

func containsToolCall(history []model.Item, callID string) bool {
	for _, item := range history {
		if item.ToolCall != nil && item.ToolCall.CallID == callID {
			return true
		}
	}
	return false
}

func containsToolResultOutputContaining(history []model.Item, callID string, output string) bool {
	for _, item := range history {
		if item.ToolResult != nil && item.ToolResult.CallID == callID && strings.Contains(item.ToolResult.Output, output) {
			return true
		}
	}
	return false
}

func containsContextContaining(history []model.Item, content string) bool {
	for _, item := range history {
		if item.Kind == model.ItemContext && strings.Contains(item.Content, content) {
			return true
		}
	}
	return false
}

func boolPtr(value bool) *bool {
	return &value
}
