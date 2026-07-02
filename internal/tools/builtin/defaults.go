package builtin

import (
	"github.com/rizrmd/dexco/internal/tools"
)

func CodingWorkflowHandlers(responder UserInputResponder) []tools.Handler {
	return []tools.Handler{
		ExecCommandHandler{},
		CurrentTimeHandler{},
		RequestUserInputHandler{Responder: responder},
		UpdatePlanHandler{},
		ViewImageHandler{},
	}
}
