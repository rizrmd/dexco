package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openai/codex/dexco/internal/model"
)

func TestExecCommandHandlerRunsCommand(t *testing.T) {
	t.Parallel()

	handler := ExecCommandHandler{}
	result, err := handler.Call(context.Background(), model.ToolCall{
		CallID:    "call-1",
		Name:      "exec_command",
		Arguments: json.RawMessage(`{"cmd":"printf 'hello'"}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	if !result.Success {
		t.Fatalf("Success = false, want true")
	}
	assertExecOutput(t, result.Output, 0, "hello")
}

// Adapted from Codex shell_command tests for shell syntax and Unicode output.
// Dexco always executes through `bash -lc`, so pipelines, quoting, and UTF-8
// output should survive into the same Codex-style result envelope.
func TestExecCommandHandlerRunsThroughShellSyntaxAndUnicode(t *testing.T) {
	t.Parallel()

	handler := ExecCommandHandler{}
	result, err := handler.Call(context.Background(), model.ToolCall{
		CallID:    "call-1",
		Name:      "exec_command",
		Arguments: json.RawMessage(`{"cmd":"printf 'naïve_café\\nsecond' | cat"}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	if !result.Success {
		t.Fatalf("Success = false, want true")
	}
	assertExecOutput(t, result.Output, 0, "naïve_café\nsecond")
}

// Adapted from Codex exec tool tests. Codex has a much richer exec runtime, but
// Dexco's built-in command handler still needs the portable basics: run through
// a shell, report exit code/wall time, and return captured stdout as the
// model-visible tool result.
func TestExecCommandHandlerRunsCommandInWorkdir(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "marker.txt"), []byte("workspace"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	handler := ExecCommandHandler{}
	args := ExecCommandArgs{
		Cmd:     "cat marker.txt",
		Workdir: workdir,
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	result, err := handler.Call(context.Background(), model.ToolCall{
		CallID:    "call-1",
		Name:      "exec_command",
		Arguments: rawArgs,
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	if !result.Success {
		t.Fatalf("Success = false, want true")
	}
	assertExecOutput(t, result.Output, 0, "workspace")
}

// Adapted from Codex shell_serialization's JSON fixture coverage. Shell output
// is a freeform transcript: even if the command prints valid JSON, Dexco must
// return the Codex-style text envelope and preserve the exact captured output
// body, including the trailing newline.
func TestExecCommandHandlerPreservesJSONOutputAsPlainText(t *testing.T) {
	t.Parallel()

	const fixtureJSON = "{\n  \"description\": \"example JSON\",\n  \"foo\": \"bar\",\n  \"isTest\": true\n}\n"
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "fixture.json"), []byte(fixtureJSON), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	handler := ExecCommandHandler{}
	args := ExecCommandArgs{
		Cmd:     "cat fixture.json",
		Workdir: workdir,
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	result, err := handler.Call(context.Background(), model.ToolCall{
		CallID:    "call-1",
		Name:      "exec_command",
		Arguments: rawArgs,
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	if !result.Success {
		t.Fatalf("Success = false, want true")
	}
	if json.Valid([]byte(result.Output)) {
		t.Fatalf("Output = %q, want freeform text envelope rather than raw JSON", result.Output)
	}
	header, body, ok := strings.Cut(result.Output, "\nOutput:\n")
	if !ok {
		t.Fatalf("Output = %q, want Output section", result.Output)
	}
	if !strings.HasPrefix(header, "Exit code: 0\nWall time: ") {
		t.Fatalf("header = %q, want exit code and wall time", header)
	}
	if body != fixtureJSON {
		t.Fatalf("Output body = %q, want exact fixture JSON %q", body, fixtureJSON)
	}
}

// Adapted from Codex shell_serialization's duration coverage. Wall time is part
// of the model-visible shell transcript, so a command that takes measurable
// time should report a positive duration in seconds.
func TestExecCommandHandlerRecordsWallTime(t *testing.T) {
	t.Parallel()

	handler := ExecCommandHandler{}
	result, err := handler.Call(context.Background(), model.ToolCall{
		CallID:    "call-1",
		Name:      "exec_command",
		Arguments: json.RawMessage(`{"cmd":"sleep 0.05"}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	if !result.Success {
		t.Fatalf("Success = false, want true")
	}
	wallTime := execOutputWallTime(t, result.Output)
	if wallTime <= 0 {
		t.Fatalf("wall time = %f, want positive duration in output %q", wallTime, result.Output)
	}
	if !strings.HasSuffix(result.Output, "\nOutput:\n") {
		t.Fatalf("Output = %q, want empty Output section", result.Output)
	}
}

// Adapted from Codex output truncation tests. Dexco exposes a compact
// max_output_chars knob rather than Codex's token policy, but the invariant is
// the same: large command output must be visibly truncated before it enters
// model-visible history.
func TestExecCommandHandlerTruncatesOutput(t *testing.T) {
	t.Parallel()

	handler := ExecCommandHandler{}
	result, err := handler.Call(context.Background(), model.ToolCall{
		CallID:    "call-1",
		Name:      "exec_command",
		Arguments: json.RawMessage(`{"cmd":"printf abcdef","max_output_chars":3}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	if !result.Success {
		t.Fatalf("Success = false, want true")
	}
	assertExecOutput(t, result.Output, 0, "a…3 chars truncated…ef")
}

// Adapted from Codex protocol's exec_output tests. Dexco's builtin exec tool
// receives raw process bytes from Go, but the model-visible transcript should
// preserve the same common Windows shell encodings that Codex handles before
// falling back to lossy UTF-8.
func TestDecodeShellOutputUsesSmartEncodingFallbacks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		bytes []byte
		want  string
	}{
		{
			name:  "utf8",
			bytes: []byte("пример"),
			want:  "пример",
		},
		{
			name:  "cp1251 cyrillic",
			bytes: []byte{0xEF, 0xF0, 0xE8, 0xEC, 0xE5, 0xF0},
			want:  "пример",
		},
		{
			name:  "cp866 cyrillic",
			bytes: []byte{0xAF, 0xE0, 0xA8, 0xAC, 0xA5, 0xE0},
			want:  "пример",
		},
		{
			name:  "windows-1252 smart punctuation",
			bytes: []byte{0x93, 0x94, ' ', 't', 'e', 's', 't', ' ', 0x96, ' ', 'd', 'a', 's', 'h'},
			want:  "“” test – dash",
		},
		{
			name:  "mixed ascii latin1",
			bytes: []byte{'O', 'u', 't', 'p', 'u', 't', ':', ' ', 'c', 'a', 'f', 0xE9},
			want:  "Output: café",
		},
		{
			name:  "pure latin1",
			bytes: []byte{'c', 'a', 'f', 0xE9},
			want:  "café",
		},
		{
			name:  "invalid bytes",
			bytes: []byte{0xFF, 0xFE, 0xFD},
			want:  strings.ToValidUTF8(string([]byte{0xFF, 0xFE, 0xFD}), "\uFFFD"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := decodeShellOutput(tt.bytes); got != tt.want {
				t.Fatalf("decodeShellOutput() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Adapted from Codex unified_exec HeadTailBuffer tests. Keeping both the start
// and end of large command output is important because failures often include
// setup context near the top and the actionable error near the bottom.
func TestTruncateStringKeepsPrefixAndSuffixWhenOverBudget(t *testing.T) {
	t.Parallel()

	got := truncateString("0123456789ab", 10)

	if !strings.HasPrefix(got, "01234") {
		t.Fatalf("truncateString() = %q, want prefix 01234", got)
	}
	if !strings.HasSuffix(got, "789ab") {
		t.Fatalf("truncateString() = %q, want suffix 789ab", got)
	}
	if !strings.Contains(got, "…2 chars truncated…") {
		t.Fatalf("truncateString() = %q, want omitted-char marker", got)
	}
}

// Adapted from Codex HeadTailBuffer's small-budget behavior. When only one
// visible character can be retained, the tail is more useful than the head for
// command output because it usually contains the final error or status.
func TestTruncateStringSingleCharacterBudgetKeepsTail(t *testing.T) {
	t.Parallel()

	got := truncateString("abc", 1)
	want := "…2 chars truncated…c"

	if got != want {
		t.Fatalf("truncateString() = %q, want %q", got, want)
	}
}

// Adapted from Codex output truncation UTF-8 coverage. Dexco truncates by rune
// count rather than bytes/tokens, so multibyte characters must not be split.
func TestTruncateStringHandlesUnicodeAtRuneBoundary(t *testing.T) {
	t.Parallel()

	got := truncateString("😀😀😀😀\nsecond", 6)
	want := "😀😀😀…5 chars truncated…ond"

	if got != want {
		t.Fatalf("truncateString() = %q, want %q", got, want)
	}
}

func TestExecCommandHandlerReturnsFailedResultOnCommandError(t *testing.T) {
	t.Parallel()

	handler := ExecCommandHandler{}
	result, err := handler.Call(context.Background(), model.ToolCall{
		CallID:    "call-1",
		Name:      "exec_command",
		Arguments: json.RawMessage(`{"cmd":"exit 7"}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	if result.Success {
		t.Fatalf("Success = true, want false")
	}
	assertExecOutput(t, result.Output, 7, "")
}

func TestExecCommandHandlerRejectsEmptyCommand(t *testing.T) {
	t.Parallel()

	handler := ExecCommandHandler{}
	_, err := handler.Call(context.Background(), model.ToolCall{
		CallID:    "call-1",
		Name:      "exec_command",
		Arguments: json.RawMessage(`{"cmd":"   "}`),
	})
	if err == nil || err.Error() != "exec_command requires non-empty cmd" {
		t.Fatalf("Call() error = %v, want empty command error", err)
	}

	_, err = handler.Guardrail(context.Background(), model.ToolCall{
		CallID:    "call-1",
		Name:      "exec_command",
		Arguments: json.RawMessage(`{"cmd":"   "}`),
	})
	if err == nil || err.Error() != "exec_command requires non-empty cmd" {
		t.Fatalf("Guardrail() error = %v, want empty command error", err)
	}
}

// Adapted from Codex core's command_canonicalization tests. Codex stores
// approval decisions against canonical argv so `/bin/bash -lc` and `bash -lc`
// wrappers do not create different approval keys. Dexco has a smaller
// exec_command surface, but it keeps the same key-stability invariant for
// request_permissions grants.
func TestCanonicalizeCommandForApprovalCollapsesPlainShellScripts(t *testing.T) {
	t.Parallel()

	commandA := []string{"/bin/bash", "-lc", "cargo test -p codex-core"}
	commandB := []string{"bash", "-lc", "cargo   test   -p codex-core"}
	want := []string{"cargo", "test", "-p", "codex-core"}

	if got := canonicalizeCommandForApproval(commandA); !reflect.DeepEqual(got, want) {
		t.Fatalf("canonicalizeCommandForApproval(commandA) = %#v, want %#v", got, want)
	}
	if gotA, gotB := canonicalizeCommandForApproval(commandA), canonicalizeCommandForApproval(commandB); !reflect.DeepEqual(gotA, gotB) {
		t.Fatalf("canonicalized commands differ: %#v != %#v", gotA, gotB)
	}
}

// Adapted from Codex core's command_canonicalization heredoc case. Complex
// shell scripts keep exact script text because approval reuse must not collapse
// commands that only look similar after lossy tokenization.
func TestCanonicalizeCommandForApprovalKeepsComplexShellScriptKey(t *testing.T) {
	t.Parallel()

	script := "python3 <<'PY'\nprint('hello')\nPY"
	commandA := []string{"/bin/zsh", "-lc", script}
	commandB := []string{"zsh", "-lc", script}
	want := []string{canonicalBashScriptPrefix, "-lc", script}

	if got := canonicalizeCommandForApproval(commandA); !reflect.DeepEqual(got, want) {
		t.Fatalf("canonicalizeCommandForApproval(commandA) = %#v, want %#v", got, want)
	}
	if gotA, gotB := canonicalizeCommandForApproval(commandA), canonicalizeCommandForApproval(commandB); !reflect.DeepEqual(gotA, gotB) {
		t.Fatalf("canonicalized commands differ: %#v != %#v", gotA, gotB)
	}
}

// Adapted from Codex core's command_canonicalization PowerShell case. Dexco's
// builtin exec_command currently runs Bash, but the canonicalization helper is
// kept wrapper-aware so future Windows/powershell runtimes can reuse the same
// approval-key path.
func TestCanonicalizeCommandForApprovalNormalizesPowerShellWrappers(t *testing.T) {
	t.Parallel()

	script := "Write-Host hi"
	commandA := []string{"powershell.exe", "-NoProfile", "-Command", script}
	commandB := []string{"powershell", "-Command", script}
	want := []string{canonicalPowerShellScriptPrefix, script}

	if got := canonicalizeCommandForApproval(commandA); !reflect.DeepEqual(got, want) {
		t.Fatalf("canonicalizeCommandForApproval(commandA) = %#v, want %#v", got, want)
	}
	if gotA, gotB := canonicalizeCommandForApproval(commandA), canonicalizeCommandForApproval(commandB); !reflect.DeepEqual(gotA, gotB) {
		t.Fatalf("canonicalized commands differ: %#v != %#v", gotA, gotB)
	}
}

func TestCanonicalizeCommandForApprovalPreservesNonShellCommands(t *testing.T) {
	t.Parallel()

	command := []string{"cargo", "fmt"}

	if got := canonicalizeCommandForApproval(command); !reflect.DeepEqual(got, command) {
		t.Fatalf("canonicalizeCommandForApproval() = %#v, want %#v", got, command)
	}
}

// Adapted from Codex unified-exec timeout coverage. Dexco's command runtime is
// intentionally smaller, but a timeout must still resolve the tool call with a
// failed result so the model can observe the failure and continue.
func TestExecCommandHandlerTimeoutReturnsFailedResult(t *testing.T) {
	t.Parallel()

	handler := ExecCommandHandler{}
	start := time.Now()
	result, err := handler.Call(context.Background(), model.ToolCall{
		CallID:    "call-1",
		Name:      "exec_command",
		Arguments: json.RawMessage(`{"cmd":"sleep 1","timeout_ms":50}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	if time.Since(start) > 2*time.Second {
		t.Fatalf("timeout command took too long")
	}
	if result.Success {
		t.Fatalf("Success = true, want false")
	}
	assertExecOutput(t, result.Output, 124, "command timed out after 50 milliseconds")
}

// Adapted from Codex shell/exec approval coverage. Dexco's builtin shell tool
// cannot provide Codex's OS sandbox, but it must classify command execution as
// approval-required so runner guardrails can stop side effects before dispatch.
func TestExecCommandHandlerGuardrailRequiresApproval(t *testing.T) {
	t.Parallel()

	handler := ExecCommandHandler{}
	guardrail, err := handler.Guardrail(context.Background(), model.ToolCall{
		CallID:    "call-1",
		Name:      "exec_command",
		Arguments: json.RawMessage(`{"cmd":"printf 'hello'"}`),
	})
	if err != nil {
		t.Fatalf("Guardrail() error = %v", err)
	}

	want := model.ToolGuardrail{
		Risk:                model.ToolRiskCommandExecution,
		ApprovalRequirement: model.ApprovalRequirementRequired,
		Reason:              `run shell command "printf 'hello'"`,
		PermissionGrantKey:  `exec_command:["__codex_shell_script__","-lc","printf 'hello'"]`,
	}
	if !reflect.DeepEqual(guardrail, want) {
		t.Fatalf("Guardrail() = %#v, want %#v", guardrail, want)
	}
}

func TestExecCommandHandlerGuardrailCanonicalizesPermissionGrantKey(t *testing.T) {
	t.Parallel()

	handler := ExecCommandHandler{}
	commandA, err := handler.Guardrail(context.Background(), model.ToolCall{
		CallID:    "call-1",
		Name:      "exec_command",
		Arguments: json.RawMessage(`{"cmd":"cargo test -p codex-core"}`),
	})
	if err != nil {
		t.Fatalf("Guardrail(commandA) error = %v", err)
	}
	commandB, err := handler.Guardrail(context.Background(), model.ToolCall{
		CallID:    "call-2",
		Name:      "exec_command",
		Arguments: json.RawMessage(`{"cmd":"cargo   test   -p codex-core"}`),
	})
	if err != nil {
		t.Fatalf("Guardrail(commandB) error = %v", err)
	}
	complex, err := handler.Guardrail(context.Background(), model.ToolCall{
		CallID:    "call-3",
		Name:      "exec_command",
		Arguments: json.RawMessage(`{"cmd":"printf 'hello'"}`),
	})
	if err != nil {
		t.Fatalf("Guardrail(complex) error = %v", err)
	}

	if commandA.PermissionGrantKey != commandB.PermissionGrantKey {
		t.Fatalf("plain command grant keys differ: %q != %q", commandA.PermissionGrantKey, commandB.PermissionGrantKey)
	}
	if commandA.PermissionGrantKey == complex.PermissionGrantKey {
		t.Fatalf("plain and complex command grant keys both = %q, want distinct keys", commandA.PermissionGrantKey)
	}
}

func assertExecOutput(t *testing.T, output string, exitCode int, stdout string) {
	t.Helper()
	prefix := fmt.Sprintf("Exit code: %d\nWall time: ", exitCode)
	if !strings.HasPrefix(output, prefix) {
		t.Fatalf("Output = %q, want prefix %q", output, prefix)
	}
	suffix := "\nOutput:\n" + stdout
	if !strings.HasSuffix(output, suffix) {
		t.Fatalf("Output = %q, want suffix %q", output, suffix)
	}
}

func execOutputWallTime(t *testing.T, output string) float64 {
	t.Helper()
	const prefix = "Exit code: 0\nWall time: "
	rest, ok := strings.CutPrefix(output, prefix)
	if !ok {
		t.Fatalf("Output = %q, want prefix %q", output, prefix)
	}
	value, _, ok := strings.Cut(rest, " seconds\nOutput:\n")
	if !ok {
		t.Fatalf("Output = %q, want wall time seconds section", output)
	}
	wallTime, err := strconv.ParseFloat(value, 64)
	if err != nil {
		t.Fatalf("ParseFloat(%q) error = %v", value, err)
	}
	return wallTime
}
