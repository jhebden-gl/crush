package agent

import (
	"context"
	"errors"
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
	blocks   [][]string // Each block is a list of deltas.
	toolCall bool
}

// scriptedModel plays a fixed sequence of steps and records every prompt.
// The last step repeats once the script runs out.
type scriptedModel struct {
	mu       sync.Mutex
	steps    []scriptedStep
	calls    int
	prompts  []string
	messages []fantasy.Prompt
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
	m.messages = append(m.messages, append(fantasy.Prompt(nil), call.Prompt...))
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

// captured returns the prompt of every model call so far, flattened to text.
func (m *scriptedModel) captured() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.prompts...)
}

// capturedMessages returns the full prompt of every model call so far, with
// roles and provider options intact.
func (m *scriptedModel) capturedMessages() []fantasy.Prompt {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]fantasy.Prompt(nil), m.messages...)
}

type echoInput struct {
	Message string `json:"message" description:"Message"`
}

func echoTool() fantasy.AgentTool {
	return fantasy.NewAgentTool("echo", "echoes", func(context.Context, echoInput, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("echoed"), nil
	})
}

// lifecycleRunner builds a Runner with one command hook on one event.
func lifecycleRunner(t *testing.T, event, command string) *hooks.Runner {
	t.Helper()
	return hooks.NewRunner(
		map[string][]config.HookConfig{event: {{Command: command}}},
		t.TempDir(), t.TempDir(),
	)
}

// newScriptedAgent returns a session agent wired to model and runner, plus
// a session that already holds one user message.
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

// assistantTexts returns the stored text of each assistant message.
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
