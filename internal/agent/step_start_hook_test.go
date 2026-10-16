package agent

import (
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

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
	require.Equal(t, []string{"", "done"}, assistantTexts(t, env, sid))
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

func TestStepStartHookWithoutContextLeavesThePromptAlone(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"done"}}}}}
	sa, sid, _ := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventStepStart, `echo '{}'`))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)

	baseline := &scriptedModel{steps: model.steps}
	saBase, sidBase, _ := newScriptedAgent(t, baseline, nil)
	_, err = saBase.Run(t.Context(), SessionAgentCall{SessionID: sidBase, Prompt: "go"})
	require.NoError(t, err)
	require.Equal(t, baseline.captured(), model.captured())
}

func TestStepStartHookSkippedForSubAgentRuns(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"done"}}}}}
	sa, sid, _ := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventStepStart, `echo '{"context":"should not appear"}'`))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go", NonInteractive: true})
	require.NoError(t, err)
	require.NotContains(t, model.captured()[0], "should not appear")
}

func TestStepStartHookContextIsFramedAndSitsAfterTheCacheMarks(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{steps: []scriptedStep{
		{toolCall: true},
		{blocks: [][]string{{"done"}}},
	}}
	sa, sid, _ := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventStepStart, `echo '{"context":"note"}'`))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)

	prompts := model.capturedMessages()
	require.Len(t, prompts, 2)
	for i, prompt := range prompts {
		require.GreaterOrEqual(t, len(prompt), 2, "call %d", i)
		hook := prompt[len(prompt)-1]
		marked := prompt[len(prompt)-2]

		// The hook context is its own last message, framed so the model
		// does not read it as something the user said.
		require.Equal(t, fantasy.MessageRoleUser, hook.Role, "call %d", i)
		require.Equal(t, "<system_reminder>note</system_reminder>", strings.TrimSpace(promptText(fantasy.Prompt{hook})), "call %d", i)

		// It is added after the cache marks, so it carries none and the
		// message before it keeps its mark.
		require.Empty(t, hook.ProviderOptions, "call %d", i)
		require.NotEmpty(t, marked.ProviderOptions, "call %d", i)
	}
}

func TestStepStartHookLeavesTheMarkedPrefixUnchanged(t *testing.T) {
	t.Parallel()

	steps := []scriptedStep{{blocks: [][]string{{"done"}}}}
	hooked := &scriptedModel{steps: steps}
	sa, sid, _ := newScriptedAgent(t, hooked, lifecycleRunner(t, hooks.EventStepStart, `echo '{"context":"note"}'`))
	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)

	plain := &scriptedModel{steps: steps}
	saPlain, sidPlain, _ := newScriptedAgent(t, plain, nil)
	_, err = saPlain.Run(t.Context(), SessionAgentCall{SessionID: sidPlain, Prompt: "go"})
	require.NoError(t, err)

	withHook := hooked.capturedMessages()[0]
	without := plain.capturedMessages()[0]
	require.Len(t, withHook, len(without)+1)
	require.Equal(t, without, withHook[:len(without)], "the hook must only append")
}

func TestStepStartHookSkippedWhenTheAgentIsASubAgent(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"done"}}}}}
	sa, sid, _ := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventStepStart, `echo '{"context":"should not appear"}'`))
	sa.isSubAgent = true

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)
	require.NotContains(t, model.captured()[0], "should not appear")
}
