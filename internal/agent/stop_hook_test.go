package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/stretchr/testify/require"
)

func (m *scriptedModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
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

func TestStopHookWithoutAReasonUsesADefaultPrompt(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"pass"}}}}}
	flag := filepath.Join(t.TempDir(), "seen")
	cmd := `if [ -f ` + flag + ` ]; then exit 0; fi; touch ` + flag + `; echo '{"decision":"block"}'`
	sa, sid, _ := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventStop, cmd))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)
	require.Equal(t, 2, model.callCount())
	require.Contains(t, model.captured()[1], "Stop hook")
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
	cmd, payloads := stopCounterHook(t, 0) // Records every Stop, never blocks.
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

func TestStopHookNotFiredAfterAHaltedTurn(t *testing.T) {
	t.Parallel()

	// A tool that halts the turn (as a PreToolUse hook halt does) ends it for
	// good. A Stop hook must not be able to restart a turn that was halted.
	haltTool := fantasy.NewAgentTool("echo", "halts", func(context.Context, echoInput, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		resp := fantasy.NewTextResponse("halted")
		resp.StopTurn = true
		return resp, nil
	})
	model := &scriptedModel{steps: []scriptedStep{{toolCall: true}}}
	cmd, payloads := stopCounterHook(t, 5)

	env := testEnv(t)
	sa := testSessionAgent(env, model, model, "system", haltTool).(*sessionAgent)
	sa.hookRunner = lifecycleRunner(t, hooks.EventStop, cmd)
	sess, err := env.sessions.Create(t.Context(), "test")
	require.NoError(t, err)
	seedUserMessage(t, env, sess.ID)

	_, err = sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go"})
	require.NoError(t, err)
	require.Equal(t, 1, model.callCount())
	_, statErr := os.Stat(payloads)
	require.True(t, os.IsNotExist(statErr), "Stop must not fire after a halt")
}

func TestStopHookIsCancelledWithTheTurn(t *testing.T) {
	t.Parallel()

	// A hook that hangs keeps the session busy, and Cancel reaches it. The
	// turn ends without a continuation instead of waiting out the hook.
	started := filepath.Join(t.TempDir(), "started")
	cmd := `touch ` + started + `; sleep 30; echo '{"decision":"block","reason":"too late"}'`
	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"pass"}}}}}
	sa, sid, _ := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventStop, cmd))

	done := make(chan error, 1)
	go func() {
		_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
		done <- err
	}()

	require.Eventually(t, func() bool {
		_, err := os.Stat(started)
		return err == nil
	}, 10*time.Second, 10*time.Millisecond, "the Stop hook never started")
	require.True(t, sa.IsSessionBusy(sid), "the session must stay busy while a Stop hook runs")

	sa.Cancel(sid)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after Cancel; the Stop hook was not cancelled")
	}
	require.Equal(t, 1, model.callCount(), "a cancelled turn must not continue")
}

func TestStopHookHaltEndsTheTurn(t *testing.T) {
	t.Parallel()

	// Exit code 49 halts. It must end the turn, not restart it with the halt
	// reason as the next prompt.
	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"pass"}}}}}
	sa, sid, _ := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventStop, `echo "stop everything" >&2; exit 49`))

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)
	require.Equal(t, 1, model.callCount())
}

func TestStopContinuationPublishesOneRunComplete(t *testing.T) {
	t.Parallel()

	// The continuation keeps the caller's OnComplete, so the coordinator sees
	// exactly one terminal event for the run, from the last pass.
	model := &scriptedModel{steps: []scriptedStep{
		{blocks: [][]string{{"first pass"}}},
		{blocks: [][]string{{"second pass"}}},
	}}
	cmd, _ := stopCounterHook(t, 1)
	sa, sid, _ := newScriptedAgent(t, model, lifecycleRunner(t, hooks.EventStop, cmd))

	var completes []notify.RunComplete
	_, err := sa.Run(t.Context(), SessionAgentCall{
		SessionID:  sid,
		Prompt:     "go",
		RunID:      "run-1",
		OnComplete: func(c notify.RunComplete) { completes = append(completes, c) },
	})
	require.NoError(t, err)
	require.Equal(t, 2, model.callCount())
	require.Len(t, completes, 1)
	require.Equal(t, "run-1", completes[0].RunID)
	require.Equal(t, "second pass", completes[0].Text)
	require.False(t, completes[0].Cancelled)
	require.Empty(t, completes[0].Error)
}

func TestStopContinuationRunsAFullTurnOfHooks(t *testing.T) {
	t.Parallel()

	// Each extra pass is a full turn, so the per-turn lifecycle hooks fire
	// for it. This pins what the docs promise.
	model := &scriptedModel{steps: []scriptedStep{{blocks: [][]string{{"pass"}}}}}
	events := filepath.Join(t.TempDir(), "events.txt")
	record := config.HookConfig{Command: `echo "$CRUSH_EVENT" >> ` + events}
	stopCmd, _ := stopCounterHook(t, 1)
	runner := hooks.NewRunner(map[string][]config.HookConfig{
		hooks.EventTurnStart:        {record},
		hooks.EventUserPromptSubmit: {record},
		hooks.EventTurnEnd:          {record},
		hooks.EventStop:             {record, {Command: stopCmd}},
	}, t.TempDir(), t.TempDir())
	sa, sid, _ := newScriptedAgent(t, model, runner)
	sa.promptHookRunner = runner

	_, err := sa.Run(t.Context(), SessionAgentCall{SessionID: sid, Prompt: "go"})
	require.NoError(t, err)

	data, err := os.ReadFile(events)
	require.NoError(t, err)
	require.Equal(t, []string{
		"UserPromptSubmit", "TurnStart", "Stop", "TurnEnd",
		"UserPromptSubmit", "TurnStart", "Stop", "TurnEnd",
	}, strings.Fields(string(data)))
}
