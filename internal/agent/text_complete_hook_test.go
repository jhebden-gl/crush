package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/stretchr/testify/require"
)

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

func TestTextCompleteHookLeavesUnchangedTextAlone(t *testing.T) {
	t.Parallel()

	marker := filepath.Join(t.TempDir(), "ran")
	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"nothing to fix"}}}}}
	sa, sid, env := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventTextComplete, `touch `+marker+`; `+useReplacer))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)
	require.FileExists(t, marker, "the hook must have run")
	require.Equal(t, []string{"nothing to fix"}, assistantTexts(t, env, sid))
}

func TestTextCompleteHookIgnoresABlankReplacement(t *testing.T) {
	t.Parallel()

	// A blank replacement would leave an empty bubble, so it counts as no
	// replacement, like an empty one.
	marker := filepath.Join(t.TempDir(), "ran")
	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"utilise"}}}}}
	sa, sid, env := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventTextComplete, `touch `+marker+`; echo '{"updated_text":" \n "}'`))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)
	require.FileExists(t, marker, "the hook must have run")
	require.Equal(t, []string{"utilise"}, assistantTexts(t, env, sid))
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

	marker := filepath.Join(t.TempDir(), "ran")
	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"utilise"}}}}}
	sa, sid, env := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventTextComplete, `touch `+marker+`; echo boom >&2; exit 9`))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)
	require.FileExists(t, marker, "the hook must have run")
	require.Equal(t, []string{"utilise"}, assistantTexts(t, env, sid))
}

func TestTextCompleteHookSkippedWhenTheAgentIsASubAgent(t *testing.T) {
	t.Parallel()

	marker := filepath.Join(t.TempDir(), "ran")
	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"utilise"}}}}}
	sa, sid, env := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventTextComplete, `touch `+marker+`; `+useReplacer))
	sa.isSubAgent = true

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)
	require.Equal(t, []string{"utilise"}, assistantTexts(t, env, sid))
	require.NoFileExists(t, marker)
}

func TestTextCompleteRewriteReachesTheModelOnTheNextRunOnly(t *testing.T) {
	t.Parallel()

	// The corrected text is stored right away, but the model's own view of
	// the current run is built from what it streamed, so later steps of the
	// same run still see the original. The next run replays the stored text.
	model := &scriptedModel{steps: []scriptedStep{
		{blocks: [][]string{{"we utilise it"}}, toolCall: true},
		{blocks: [][]string{{"done"}}},
		{blocks: [][]string{{"ok"}}},
	}}
	sa, sid, env := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventTextComplete, useReplacer))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "first"})
	require.NoError(t, err)
	require.Equal(t, "we use it", assistantTexts(t, env, sid)[0])

	_, err = sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "second"})
	require.NoError(t, err)

	prompts := model.captured()
	require.Len(t, prompts, 3)
	require.Contains(t, prompts[1], "utilise", "later steps of the same run see the streamed text")
	require.Contains(t, prompts[2], "we use it")
	require.NotContains(t, prompts[2], "utilise", "the next run replays the stored text")
}
