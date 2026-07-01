package events

import (
	"context"

	"github.com/openai/codex/dexco/internal/model"
)

type Sink interface {
	OnTurnStarted(ctx context.Context, turn model.Turn) error
	OnTextDelta(ctx context.Context, turnID string, delta string) error
	OnReasoningDelta(ctx context.Context, turnID string, delta string) error
	OnToolCall(ctx context.Context, turnID string, call model.ToolCall) error
	OnToolResult(ctx context.Context, turnID string, result model.ToolResult) error
	OnTurnCompleted(ctx context.Context, turn model.Turn) error
}

type ResponseEventSink interface {
	OnResponseEvent(ctx context.Context, turnID string, event model.ResponseEvent) error
}

type ClientEventSink interface {
	OnClientEvent(ctx context.Context, event model.ClientEvent) error
}

type NopSink struct{}

func (NopSink) OnTurnStarted(context.Context, model.Turn) error {
	return nil
}

func (NopSink) OnTextDelta(context.Context, string, string) error {
	return nil
}

func (NopSink) OnReasoningDelta(context.Context, string, string) error {
	return nil
}

func (NopSink) OnToolCall(context.Context, string, model.ToolCall) error {
	return nil
}

func (NopSink) OnToolResult(context.Context, string, model.ToolResult) error {
	return nil
}

func (NopSink) OnTurnCompleted(context.Context, model.Turn) error {
	return nil
}
