package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// scriptedStep is one model call: some text blocks, optionally followed by a
// tool call that forces a second step.
type scriptedStep struct {
	blocks   [][]string // each block is a list of deltas
	toolCall bool
}

// scriptedModel plays a fixed sequence of steps and records every prompt.
type scriptedModel struct {
	mu      sync.Mutex
	steps   []scriptedStep
	calls   int
	prompts []string
}

func (m *scriptedModel) Provider() string { return "fake" }
func (m *scriptedModel) Model() string    { return "fake-model" }

func (m *scriptedModel) Generate(context.Context, fantasy.Call) (*fantasy.Response, error) {
	return nil, errors.New("not implemented")
}

func (m *scriptedModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *scriptedModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *scriptedModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	idx := m.calls
	m.calls++
	m.prompts = append(m.prompts, promptText(call.Prompt))
	step := m.steps[min(idx, len(m.steps)-1)]
	m.mu.Unlock()

	return func(yield func(fantasy.StreamPart) bool) {
		for i, block := range step.blocks {
			id := "text-" + string(rune('a'+i))
			if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: id}) {
				return
			}
			for _, delta := range block {
				if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: id, Delta: delta}) {
					return
				}
			}
			if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: id}) {
				return
			}
		}
		if step.toolCall {
			if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputStart, ID: "tool-1", ToolCallName: "echo"}) {
				return
			}
			if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputEnd, ID: "tool-1"}) {
				return
			}
			if !yield(fantasy.StreamPart{
				Type:          fantasy.StreamPartTypeToolCall,
				ID:            "tool-1",
				ToolCallName:  "echo",
				ToolCallInput: `{"message":"hi"}`,
			}) {
				return
			}
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonToolCalls})
			return
		}
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
	}, nil
}

func (m *scriptedModel) captured() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.prompts...)
}

func (m *scriptedModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

type echoInput struct {
	Message string `json:"message" description:"Message"`
}

func echoTool() fantasy.AgentTool {
	return fantasy.NewAgentTool("echo", "echoes", func(context.Context, echoInput, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("echoed"), nil
	})
}

func lifecycleRunner(t *testing.T, event, command string) *hooks.Runner {
	t.Helper()
	return hooks.NewRunner(
		map[string][]config.HookConfig{event: {{Command: command}}},
		t.TempDir(), t.TempDir(),
	)
}

func newScriptedAgent(t *testing.T, model *scriptedModel, runner *hooks.Runner) (*sessionAgent, string, fakeEnv) {
	t.Helper()
	env := testEnv(t)
	sa := testSessionAgent(env, model, model, "system", echoTool()).(*sessionAgent)
	sa.hookRunner = runner
	sess, err := env.sessions.Create(t.Context(), "test")
	require.NoError(t, err)
	seedUserMessage(t, env, sess.ID)
	return sa, sess.ID, env
}

func assistantTexts(t *testing.T, env fakeEnv, sessionID string) []string {
	t.Helper()
	msgs, err := env.messages.List(t.Context(), sessionID)
	require.NoError(t, err)
	var out []string
	for _, m := range msgs {
		if m.Role == message.Assistant {
			out = append(out, m.Content().Text)
		}
	}
	return out
}

const useReplacer = `jq -c '{updated_text: (.text | gsub("utilise";"use"))}'`

func TestTextCompleteHookRewritesStoredAndReplayedText(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"we util", "ise it"}}}}}
	sa, sid, env := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventTextComplete, useReplacer))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "first"})
	require.NoError(t, err)
	require.Equal(t, []string{"we use it"}, assistantTexts(t, env, sid))

	// The corrected text is what the model sees on the next turn.
	_, err = sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "second"})
	require.NoError(t, err)
	prompts := model.captured()
	require.Len(t, prompts, 2)
	require.Contains(t, prompts[1], "we use it")
	require.NotContains(t, prompts[1], "utilise")
}

func TestTextCompleteHookRewritesEachBlockInPlace(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"aaa utilise"}, {" bbb utilise"}}}}}
	sa, sid, env := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventTextComplete, useReplacer))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)
	require.Equal(t, []string{"aaa use bbb use"}, assistantTexts(t, env, sid))
}

func TestTextCompleteHookSkippedForSubAgentRuns(t *testing.T) {
	t.Parallel()

	marker := filepath.Join(t.TempDir(), "ran")
	cmd := `touch ` + marker + `; ` + useReplacer
	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"utilise"}}}}}
	sa, sid, env := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventTextComplete, cmd))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go", NonInteractive: true})
	require.NoError(t, err)
	require.Equal(t, []string{"utilise"}, assistantTexts(t, env, sid))
	_, statErr := os.Stat(marker)
	require.True(t, os.IsNotExist(statErr), "sub-agent turns must not fire TextComplete")
}

func TestTextCompleteHookFailureKeepsOriginalText(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"utilise"}}}}}
	sa, sid, env := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventTextComplete, `echo boom >&2; exit 9`))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)
	require.Equal(t, []string{"utilise"}, assistantTexts(t, env, sid))
}

func TestStepStartHookContextReachesEachModelCallOnly(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{steps: []scriptedStep{
		{toolCall: true},
		{blocks: [][]string{{"done"}}},
	}}
	cmd := `jq -c '{context: ("note for step " + (.step|tostring))}'`
	sa, sid, env := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventStepStart, cmd))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)

	prompts := model.captured()
	require.Len(t, prompts, 2)
	require.Contains(t, prompts[0], "note for step 0")
	require.Contains(t, prompts[1], "note for step 1")
	require.NotContains(t, prompts[1], "note for step 0", "step context must not accumulate")

	// Nothing from the hook is persisted in the transcript.
	msgs, err := env.messages.List(t.Context(), sid)
	require.NoError(t, err)
	for _, m := range msgs {
		for _, part := range m.Parts {
			if tc, ok := part.(message.TextContent); ok {
				require.NotContains(t, tc.Text, "note for step")
			}
		}
	}
}

func TestStepStartHookSkippedForSubAgentRuns(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"done"}}}}}
	sa, sid, _ := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventStepStart, `echo '{"context":"should not appear"}'`))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go", NonInteractive: true})
	require.NoError(t, err)
	require.NotContains(t, model.captured()[0], "should not appear")
}

// stopCounterHook blocks the first n stops, recording each payload.
func stopCounterHook(t *testing.T, blocks int) (command, payloads string) {
	t.Helper()
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	payloads = filepath.Join(dir, "payloads.jsonl")
	command = `payload=$(cat); echo "$payload" >> ` + payloads + `;
n=$(cat ` + counter + ` 2>/dev/null || echo 0); n=$((n+1)); echo $n > ` + counter + `;
if [ $n -le ` + strconv.Itoa(blocks) + ` ]; then echo '{"decision":"block","reason":"again please"}'; fi`
	return command, payloads
}

func TestStopHookContinuesTheTurnWithItsReason(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"first pass"}}}}}
	cmd, payloads := stopCounterHook(t, 1)
	sa, sid, env := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventStop, cmd))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)

	require.Equal(t, 2, model.callCount())
	require.Contains(t, model.captured()[1], "again please")
	require.Len(t, assistantTexts(t, env, sid), 2)

	data, err := os.ReadFile(payloads)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)
	var first, second map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &second))
	require.Equal(t, false, first["stop_hook_active"])
	require.Equal(t, true, second["stop_hook_active"])
	require.Equal(t, "first pass", first["text"])
}

func TestStopHookCapsContinuations(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"again"}}}}}
	cmd, _ := stopCounterHook(t, 1000)
	sa, sid, _ := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventStop, cmd))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)
	require.Equal(t, 1+maxStopContinuations, model.callCount())
}

func TestStopHookNotFiredForSubAgentRuns(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"done"}}}}}
	cmd, payloads := stopCounterHook(t, 5)
	sa, sid, _ := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventStop, cmd))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go", NonInteractive: true})
	require.NoError(t, err)
	require.Equal(t, 1, model.callCount())
	_, statErr := os.Stat(payloads)
	require.True(t, os.IsNotExist(statErr))
}

func TestStopHookExitCodeTwoAlsoContinues(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"pass"}}}}}
	flag := filepath.Join(t.TempDir(), "seen")
	cmd := `if [ -f ` + flag + ` ]; then exit 0; fi; touch ` + flag + `; echo "run the tests" >&2; exit 2`
	sa, sid, _ := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventStop, cmd))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)
	require.Equal(t, 2, model.callCount())
	require.Contains(t, model.captured()[1], "run the tests")
}

func TestStopHookSkippedWhileAUserPromptIsQueued(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"pass"}}}}}
	cmd, payloads := stopCounterHook(t, 0) // records every Stop, never blocks
	sa, sid, _ := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventStop, cmd))

	// A prompt with its own RunID stays queued behind the active turn. The
	// first turn must not run Stop while it waits; only the last turn does.
	sa.enqueueCall(SessionAgentCall{SessionID: sid, Prompt: "user follow-up", RunID: "r2"})
	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)
	require.Equal(t, 2, model.callCount())

	data, err := os.ReadFile(payloads)
	require.NoError(t, err)
	require.Len(t, strings.Split(strings.TrimSpace(string(data)), "\n"), 1)
}
