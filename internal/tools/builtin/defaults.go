package builtin

import (
	"github.com/openai/codex/dexco/internal/tools"
)

func DefaultHandlers(responder UserInputResponder) []tools.Handler {
	return []tools.Handler{
		ExecCommandHandler{},
		CurrentTimeHandler{},
		RequestUserInputHandler{Responder: responder},
		UpdatePlanHandler{},
		ViewImageHandler{},
	}
}
