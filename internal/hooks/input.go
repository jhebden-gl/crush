package hooks

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/charmbracelet/crush/internal/shell"
	"github.com/tidwall/gjson"
)

// SupportedOutputVersion is the highest envelope version this build
// understands. Hooks may omit `version` entirely (treated as 1) or pin
// an older version. Unknown higher versions are still parsed but logged.
const SupportedOutputVersion = 1

// Payload is the JSON structure piped to hook commands via stdin.
// ToolInput is emitted as a parsed JSON object for compatibility with
// Claude Code hooks (which expect tool_input to be an object, not a
// string). The event name is emitted twice for the same reason: a Claude
// Code hook is a single command wired to every event and reads
// hook_event_name to tell them apart, so without that key it takes its
// default branch and silently does nothing.
type Payload struct {
	Event         string          `json:"event"`
	HookEventName string          `json:"hook_event_name"`
	SessionID     string          `json:"session_id"`
	CWD           string          `json:"cwd"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
}

// PromptPayload is the JSON structure piped to UserPromptSubmit hook
// commands via stdin. There is no tool at this point in the turn, so the
// payload carries the prompt instead of tool_name/tool_input.
type PromptPayload struct {
	Event         string `json:"event"`
	HookEventName string `json:"hook_event_name"`
	SessionID     string `json:"session_id"`
	CWD           string `json:"cwd"`
	Prompt        string `json:"prompt"`
}

// BuildPayload constructs the JSON stdin payload for a hook command.
func BuildPayload(eventName, sessionID, cwd, toolName, toolInputJSON string) []byte {
	toolInput := json.RawMessage(toolInputJSON)
	if !json.Valid(toolInput) {
		toolInput = json.RawMessage("{}")
	}
	p := Payload{
		Event:         eventName,
		HookEventName: eventName,
		SessionID:     sessionID,
		CWD:           cwd,
		ToolName:      toolName,
		ToolInput:     toolInput,
	}
	data, err := json.Marshal(p)
	if err != nil {
		return []byte("{}")
	}
	return data
}

// PostToolUsePayload extends Payload with the tool's output so hooks
// can inspect or redact what the model sees.
type PostToolUsePayload struct {
	Payload
	ToolOutput string `json:"tool_output"`
	IsError    bool   `json:"is_error,omitempty"`
}

// BuildPostToolUsePayload constructs the JSON stdin payload for a
// PostToolUse hook, including the tool's response content.
func BuildPostToolUsePayload(sessionID, cwd, toolName, toolInputJSON, toolOutput string, isError bool) []byte {
	toolInput := json.RawMessage(toolInputJSON)
	if !json.Valid(toolInput) {
		toolInput = json.RawMessage("{}")
	}
	p := PostToolUsePayload{
		Payload: Payload{
			Event:     EventPostToolUse,
			SessionID: sessionID,
			CWD:       cwd,
			ToolName:  toolName,
			ToolInput: toolInput,
		},
		ToolOutput: toolOutput,
		IsError:    isError,
	}
	data, err := json.Marshal(p)
	if err != nil {
		return []byte("{}")
	}
	return data
}

// LifecyclePayload is the JSON structure piped to hook commands via
// stdin for non-tool lifecycle events (SessionStart, TurnStart, etc.).
// ToolName and ToolInput are omitted since they don't apply.
type LifecyclePayload struct {
	Event         string `json:"event"`
	HookEventName string `json:"hook_event_name"`
	SessionID     string `json:"session_id"`
	CWD           string `json:"cwd"`
}

// lifecycle builds the common part of every non-tool payload. Event and
// HookEventName carry the same value: Crush's own key and the one Claude
// Code hooks read.
func lifecycle(eventName, sessionID, cwd string) LifecyclePayload {
	return LifecyclePayload{
		Event:         eventName,
		HookEventName: eventName,
		SessionID:     sessionID,
		CWD:           cwd,
	}
}

// marshalPayload encodes a payload, falling back to an empty object so a
// hook always gets valid JSON on stdin.
func marshalPayload(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return data
}

// StepStartPayload extends LifecyclePayload with the zero-based index
// of the model call within the turn.
type StepStartPayload struct {
	LifecyclePayload
	Step int `json:"step"`
}

// StopPayload extends LifecyclePayload with the turn's final text and
// whether this stop was itself triggered by a Stop hook, so a hook can
// avoid asking for continuation forever.
type StopPayload struct {
	LifecyclePayload
	Text           string `json:"text"`
	StopHookActive bool   `json:"stop_hook_active"`
}

// TurnEndPayload extends LifecyclePayload with the assistant's
// rendered text content for the completed turn. Observe-only;
// hooks cannot modify or block based on this data.
type TurnEndPayload struct {
	LifecyclePayload
	Text string `json:"text"`
}

// BuildLifecyclePayload constructs the JSON stdin payload for a
// lifecycle hook command (non-tool events).
func BuildLifecyclePayload(eventName, sessionID, cwd string) []byte {
	return marshalPayload(lifecycle(eventName, sessionID, cwd))
}

// BuildTurnEndPayload constructs the JSON stdin payload for a
// TurnEnd hook, including the assistant's rendered text content.
func BuildTurnEndPayload(sessionID, cwd, text string) []byte {
	return marshalPayload(TurnEndPayload{
		LifecyclePayload: lifecycle(EventTurnEnd, sessionID, cwd),
		Text:             text,
	})
}

// BuildStepStartPayload constructs the stdin payload for a StepStart
// hook.
func BuildStepStartPayload(sessionID, cwd string, step int) []byte {
	return marshalPayload(StepStartPayload{
		LifecyclePayload: lifecycle(EventStepStart, sessionID, cwd),
		Step:             step,
	})
}

// BuildStopPayload constructs the stdin payload for a Stop hook.
func BuildStopPayload(sessionID, cwd, text string, stopHookActive bool) []byte {
	return marshalPayload(StopPayload{
		LifecyclePayload: lifecycle(EventStop, sessionID, cwd),
		Text:             text,
		StopHookActive:   stopHookActive,
	})
}

// BuildLifecycleEnv constructs the environment variable slice for a
// lifecycle hook command. Includes core env vars but omits
// tool-specific ones (CRUSH_TOOL_NAME, CRUSH_TOOL_INPUT_*).
func BuildLifecycleEnv(eventName, sessionID, cwd, projectDir string) []string {
	env := os.Environ()
	env = append(env, shell.CrushEnvMarkers()...)
	env = append(
		env,
		fmt.Sprintf("CRUSH_EVENT=%s", eventName),
		fmt.Sprintf("CRUSH_SESSION_ID=%s", sessionID),
		fmt.Sprintf("CRUSH_CWD=%s", cwd),
		fmt.Sprintf("CRUSH_PROJECT_DIR=%s", projectDir),
	)
	return env
}

// BuildEnv constructs the environment variable slice for a hook command.
// It includes all current process env vars plus hook-specific ones.
func BuildEnv(eventName, toolName, sessionID, cwd, projectDir, toolInputJSON string) []string {
	env := os.Environ()
	env = append(env, shell.CrushEnvMarkers()...)
	env = append(
		env,
		fmt.Sprintf("CRUSH_EVENT=%s", eventName),
		fmt.Sprintf("CRUSH_TOOL_NAME=%s", toolName),
		fmt.Sprintf("CRUSH_SESSION_ID=%s", sessionID),
		fmt.Sprintf("CRUSH_CWD=%s", cwd),
		fmt.Sprintf("CRUSH_PROJECT_DIR=%s", projectDir),
	)

	// Extract tool-specific env vars from the JSON input.
	if toolInputJSON != "" {
		if cmd := gjson.Get(toolInputJSON, "command"); cmd.Exists() {
			env = append(env, fmt.Sprintf("CRUSH_TOOL_INPUT_COMMAND=%s", cmd.String()))
		}
		if fp := gjson.Get(toolInputJSON, "file_path"); fp.Exists() {
			env = append(env, fmt.Sprintf("CRUSH_TOOL_INPUT_FILE_PATH=%s", fp.String()))
		}
	}

	return env
}

// BuildPromptPayload constructs the JSON stdin payload for a
// UserPromptSubmit hook command.
func BuildPromptPayload(eventName, sessionID, cwd, prompt string) []byte {
	return marshalPayload(PromptPayload{
		Event:         eventName,
		HookEventName: eventName,
		SessionID:     sessionID,
		CWD:           cwd,
		Prompt:        prompt,
	})
}

// BuildPromptEnv constructs the environment variable slice for a
// UserPromptSubmit hook command. Tool-specific variables are omitted
// because no tool is being called.
func BuildPromptEnv(eventName, sessionID, cwd, projectDir string) []string {
	env := os.Environ()
	env = append(env, shell.CrushEnvMarkers()...)
	env = append(
		env,
		fmt.Sprintf("CRUSH_EVENT=%s", eventName),
		fmt.Sprintf("CRUSH_SESSION_ID=%s", sessionID),
		fmt.Sprintf("CRUSH_CWD=%s", cwd),
		fmt.Sprintf("CRUSH_PROJECT_DIR=%s", projectDir),
	)
	return env
}

// parseStdout parses the JSON output from a hook command's stdout.
// Supports both Crush format and Claude Code format (hookSpecificOutput).
func parseStdout(stdout string) HookResult {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return HookResult{Decision: DecisionNone}
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		return HookResult{Decision: DecisionNone}
	}

	// Claude Code compat: if hookSpecificOutput is present, parse that.
	if hso, ok := raw["hookSpecificOutput"]; ok {
		return parseClaudeCodeOutput(hso)
	}

	var parsed struct {
		Version      int             `json:"version"`
		Decision     string          `json:"decision"`
		Halt         bool            `json:"halt"`
		Reason       string          `json:"reason"`
		Context      json.RawMessage `json:"context"`
		UpdatedInput json.RawMessage `json:"updated_input"`
	}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		return HookResult{Decision: DecisionNone}
	}

	if parsed.Version > SupportedOutputVersion {
		slog.Debug(
			"Hook output declared a newer envelope version than this build supports",
			"version", parsed.Version,
			"supported", SupportedOutputVersion,
		)
	}

	result := HookResult{
		Halt:    parsed.Halt,
		Reason:  parsed.Reason,
		Context: parseContext(parsed.Context),
	}
	result.Decision = parseDecision(parsed.Decision)
	result.Block = strings.EqualFold(parsed.Decision, "block")
	result.UpdatedInput = rawToString(parsed.UpdatedInput)
	return result
}

// parseContext accepts either a single string or an array of strings and
// returns a newline-joined value with empty entries dropped.
func parseContext(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// String form.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		return ""
	}
	// Array form.
	if raw[0] == '[' {
		var items []string
		if err := json.Unmarshal(raw, &items); err != nil {
			return ""
		}
		out := items[:0]
		for _, s := range items {
			if s != "" {
				out = append(out, s)
			}
		}
		return strings.Join(out, "\n")
	}
	return ""
}

// parseClaudeCodeOutput handles the Claude Code hook output format:
// {"hookSpecificOutput": {"permissionDecision": "allow", ...}}
func parseClaudeCodeOutput(data json.RawMessage) HookResult {
	var hso struct {
		PermissionDecision       string          `json:"permissionDecision"`
		PermissionDecisionReason string          `json:"permissionDecisionReason"`
		UpdatedInput             json.RawMessage `json:"updatedInput"`
		AdditionalContext        string          `json:"additionalContext"`
	}
	if err := json.Unmarshal(data, &hso); err != nil {
		return HookResult{Decision: DecisionNone}
	}

	result := HookResult{
		Decision: parseDecision(hso.PermissionDecision),
		Reason:   hso.PermissionDecisionReason,
		Context:  hso.AdditionalContext,
	}

	// Marshal updatedInput back to a string for our opaque format.
	if len(hso.UpdatedInput) > 0 && string(hso.UpdatedInput) != "null" {
		result.UpdatedInput = string(hso.UpdatedInput)
	}

	return result
}

// rawToString converts a json.RawMessage to a string suitable for use
// as opaque tool input. It accepts both a JSON object (nested) and a
// JSON string (stringified, for backward compatibility).
func rawToString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// If it's a JSON string, unwrap it.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	}
	// Otherwise it's an object/array — use as-is.
	return string(raw)
}

func parseDecision(s string) Decision {
	switch strings.ToLower(s) {
	case "allow":
		return DecisionAllow
	case "deny":
		return DecisionDeny
	default:
		return DecisionNone
	}
}
