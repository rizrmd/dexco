package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/rizrmd/dexco/internal/model"
)

type ExecCommandHandler struct{}

type ExecCommandArgs struct {
	Cmd            string `json:"cmd"`
	Workdir        string `json:"workdir,omitempty"`
	MaxOutputChars int    `json:"max_output_chars,omitempty"`
	TimeoutMS      int    `json:"timeout_ms,omitempty"`
}

func (ExecCommandHandler) Name() string {
	return "exec_command"
}

func (ExecCommandHandler) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        "exec_command",
		Description: "Runs a shell command and returns its captured output.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cmd": map[string]any{
					"type":        "string",
					"description": "Shell command to execute.",
				},
				"workdir": map[string]any{
					"type":        "string",
					"description": "Optional working directory for the command.",
				},
				"max_output_chars": map[string]any{
					"type":        "number",
					"description": "Optional output truncation limit.",
				},
				"timeout_ms": map[string]any{
					"type":        "number",
					"description": "Optional timeout in milliseconds.",
				},
			},
			"required": []string{"cmd"},
		},
	}
}

func (ExecCommandHandler) Guardrail(_ context.Context, call model.ToolCall) (model.ToolGuardrail, error) {
	var args ExecCommandArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return model.ToolGuardrail{}, fmt.Errorf("parse exec_command arguments: %w", err)
	}
	cmd := strings.TrimSpace(args.Cmd)
	if cmd == "" {
		return model.ToolGuardrail{}, fmt.Errorf("exec_command requires non-empty cmd")
	}
	// Codex routes shell execution through approval/sandbox policy because it is
	// the broadest local side-effect surface. Dexco does not embed a sandbox, but
	// it preserves the same pre-dispatch approval seam for library callers.
	return model.ToolGuardrail{
		Risk:                model.ToolRiskCommandExecution,
		ApprovalRequirement: model.ApprovalRequirementRequired,
		Reason:              fmt.Sprintf("run shell command %q", cmd),
		// Codex canonicalizes shell argv before approval-cache matching so an
		// approval is not tied to incidental wrapper paths or whitespace. Dexco's
		// exec_command API accepts a shell string rather than argv, so we adapt
		// the same rule by canonicalizing the implicit `bash -lc <cmd>` wrapper
		// into an opaque request_permissions key.
		PermissionGrantKey: execCommandPermissionGrantKey(cmd),
	}, nil
}

func (ExecCommandHandler) Call(ctx context.Context, call model.ToolCall) (model.ToolResult, error) {
	var args ExecCommandArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return model.ToolResult{}, fmt.Errorf("parse exec_command arguments: %w", err)
	}
	if strings.TrimSpace(args.Cmd) == "" {
		return model.ToolResult{}, fmt.Errorf("exec_command requires non-empty cmd")
	}

	if args.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(args.TimeoutMS)*time.Millisecond)
		defer cancel()
	}

	start := time.Now()
	cmd := exec.CommandContext(ctx, "bash", "-lc", args.Cmd)
	if args.Workdir != "" {
		cmd.Dir = args.Workdir
	}
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	// Codex preserves shell output as freeform text, including trailing newlines.
	// This matters when the command prints a complete JSON/YAML/file fixture:
	// the model-visible result must be the shell transcript envelope plus the
	// exact captured body, not a parsed JSON value and not a trimmed variant.
	text := decodeShellOutput(output)
	if args.MaxOutputChars > 0 && len(text) > args.MaxOutputChars {
		text = truncateString(text, args.MaxOutputChars)
	}

	exitCode := 0
	success := true
	if err != nil {
		success = false
		exitCode = 1
		var exitErr *exec.ExitError
		isExitError := errors.As(err, &exitErr)
		if isExitError {
			exitCode = exitErr.ExitCode()
		}
		if ctx.Err() == context.DeadlineExceeded {
			exitCode = 124
			text = fmt.Sprintf("command timed out after %d milliseconds", args.TimeoutMS)
		} else if text == "" && !isExitError {
			text = err.Error()
		}
	}

	return model.ToolResult{
		CallID:  call.CallID,
		Name:    call.Name,
		Output:  formatExecCommandOutput(exitCode, elapsed, text),
		Success: success,
	}, nil
}

func formatExecCommandOutput(exitCode int, elapsed time.Duration, output string) string {
	// Codex shell outputs are intentionally self-describing in model-visible
	// history. Dexco keeps the same envelope so future Codex exec improvements
	// can be adopted inside the envelope without changing the tool contract.
	return fmt.Sprintf(
		"Exit code: %d\nWall time: %.3f seconds\nOutput:\n%s",
		exitCode,
		elapsed.Seconds(),
		output,
	)
}

func truncateString(value string, maxChars int) string {
	if maxChars <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxChars {
		return value
	}
	// Codex's exec/unified-exec paths preserve both the start and end of large
	// command output, omitting the middle with an explicit truncation marker. The
	// start usually contains the command's context, while the tail often contains
	// the actionable failure. Dexco keeps the same diagnostic shape with a
	// char-based cap because its builtin exec tool is provider-neutral and does
	// not own Codex's token estimator.
	headChars := maxChars / 2
	tailChars := maxChars - headChars
	omittedChars := len(runes) - headChars - tailChars
	return fmt.Sprintf(
		"%s…%d chars truncated…%s",
		string(runes[:headChars]),
		omittedChars,
		string(runes[len(runes)-tailChars:]),
	)
}

const (
	canonicalBashScriptPrefix       = "__codex_shell_script__"
	canonicalPowerShellScriptPrefix = "__codex_powershell_script__"
)

func execCommandPermissionGrantKey(script string) string {
	key, err := json.Marshal(canonicalizeCommandForApproval([]string{"bash", "-lc", script}))
	if err != nil {
		return ""
	}
	return "exec_command:" + string(key)
}

func canonicalizeCommandForApproval(command []string) []string {
	if plain, ok := parsePlainShellLCCommand(command); ok {
		return plain
	}

	if shellMode, script, ok := extractBashCommand(command); ok {
		return []string{canonicalBashScriptPrefix, shellMode, script}
	}

	if script, ok := extractPowerShellCommand(command); ok {
		return []string{canonicalPowerShellScriptPrefix, script}
	}

	return append([]string(nil), command...)
}

func parsePlainShellLCCommand(command []string) ([]string, bool) {
	_, script, ok := extractBashCommand(command)
	if !ok {
		return nil, false
	}
	fields, ok := plainShellFields(script)
	if !ok || len(fields) == 0 {
		return nil, false
	}
	return fields, true
}

func extractBashCommand(command []string) (string, string, bool) {
	if len(command) < 3 || !isBashShell(command[0]) {
		return "", "", false
	}
	mode := command[1]
	if mode != "-lc" && mode != "-c" {
		return "", "", false
	}
	return mode, command[2], true
}

func extractPowerShellCommand(command []string) (string, bool) {
	if len(command) < 3 || !isPowerShell(command[0]) {
		return "", false
	}
	for index := 1; index < len(command)-1; index++ {
		arg := strings.ToLower(command[index])
		if arg == "-command" || arg == "-c" {
			return command[index+1], true
		}
	}
	return "", false
}

func isBashShell(program string) bool {
	switch strings.ToLower(filepath.Base(program)) {
	case "bash", "sh", "zsh":
		return true
	default:
		return false
	}
}

func isPowerShell(program string) bool {
	switch strings.ToLower(filepath.Base(program)) {
	case "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		return true
	default:
		return false
	}
}

func plainShellFields(script string) ([]string, bool) {
	if strings.TrimSpace(script) == "" {
		return nil, false
	}
	// This is intentionally conservative. Codex delegates to a shell parser for
	// the Rust implementation; Dexco only collapses scripts that are plain words
	// separated by whitespace and leaves quotes, redirects, pipelines, variable
	// expansion, and other shell syntax as exact script keys.
	for _, char := range script {
		if unicode.IsSpace(char) {
			continue
		}
		if unicode.IsControl(char) || strings.ContainsRune(`'"\\;&|<>$(){}[]*?!~`+"`", char) {
			return nil, false
		}
	}
	return strings.Fields(script), true
}
