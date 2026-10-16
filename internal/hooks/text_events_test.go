package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

func runnerFor(t *testing.T, event string, hooks ...config.HookConfig) *Runner {
	t.Helper()
	return NewRunner(map[string][]config.HookConfig{event: hooks}, t.TempDir(), t.TempDir())
}

func TestParseStdoutUpdatedText(t *testing.T) {
	t.Parallel()

	got := parseStdout(`{"updated_text":"cleaned text"}`)
	require.Equal(t, "cleaned text", got.UpdatedText)
	require.Equal(t, DecisionNone, got.Decision)

	// A non-string value is ignored rather than coerced.
	require.Empty(t, parseStdout(`{"updated_text":{"a":1}}`).UpdatedText)
	require.Empty(t, parseStdout(`{"updated_text":null}`).UpdatedText)
}

func TestParseStdoutBlockDecision(t *testing.T) {
	t.Parallel()

	got := parseStdout(`{"decision":"block","reason":"work remains"}`)
	require.True(t, got.Block)
	require.Equal(t, "work remains", got.Reason)
	// Block is a separate outcome from deny; a Stop hook asks for another pass.
	require.Equal(t, DecisionNone, got.Decision)

	require.False(t, parseStdout(`{"decision":"approve"}`).Block)
	require.False(t, parseStdout(`{}`).Block)
}

func TestAggregateUpdatedTextLastWriterWins(t *testing.T) {
	t.Parallel()

	agg := aggregate([]HookResult{
		{UpdatedText: "first"},
		{},
		{UpdatedText: "third"},
	}, "", false)
	require.Equal(t, "third", agg.UpdatedText)

	require.Empty(t, aggregate([]HookResult{{}, {}}, "", false).UpdatedText)
}

func TestAggregateBlockIsStickyAndJoinsReasons(t *testing.T) {
	t.Parallel()

	agg := aggregate([]HookResult{
		{Block: true, Reason: "tests failing"},
		{},
		{Block: true, Reason: "docs stale"},
	}, "", false)
	require.True(t, agg.Block)
	require.Equal(t, "tests failing\ndocs stale", agg.Reason)
}

func TestRunTextCompleteSendsPayloadAndReturnsReplacement(t *testing.T) {
	out := filepath.Join(t.TempDir(), "payload.json")
	t.Setenv("CRUSH_TEXT_HOOK_OUT", out)

	r := runnerFor(t, EventTextComplete, config.HookConfig{
		Command: `cat > "$CRUSH_TEXT_HOOK_OUT"; echo '{"updated_text":"use it"}'`,
	})
	agg := r.RunTextComplete(context.Background(), "sess-1", "msg-9", "utilise it")
	require.Equal(t, "use it", agg.UpdatedText)
	require.Equal(t, 1, agg.HookCount)

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "TextComplete", payload["event"])
	require.Equal(t, "TextComplete", payload["hook_event_name"])
	require.Equal(t, "sess-1", payload["session_id"])
	require.Equal(t, "msg-9", payload["message_id"])
	require.Equal(t, "utilise it", payload["text"])
	require.NotEmpty(t, payload["cwd"])
}

func TestRunTextCompleteWithoutHooksIsANoop(t *testing.T) {
	t.Parallel()

	agg := NewRunner(nil, t.TempDir(), t.TempDir()).RunTextComplete(context.Background(), "s", "m", "text")
	require.Equal(t, 0, agg.HookCount)
	require.Empty(t, agg.UpdatedText)
}

func TestRunTextCompleteFailingHookLeavesTextAlone(t *testing.T) {
	t.Parallel()

	r := runnerFor(t, EventTextComplete, config.HookConfig{Command: `echo oops >&2; exit 7`})
	agg := r.RunTextComplete(context.Background(), "s", "m", "text")
	require.Empty(t, agg.UpdatedText)
}

func TestRunStepStartSendsStepAndAggregatesContext(t *testing.T) {
	out := filepath.Join(t.TempDir(), "payload.json")
	t.Setenv("CRUSH_STEP_HOOK_OUT", out)

	r := runnerFor(t, EventStepStart,
		config.HookConfig{Command: `cat > "$CRUSH_STEP_HOOK_OUT"; echo '{"context":"note one"}'`},
		config.HookConfig{Command: `echo '{"context":["note two"]}'`},
	)
	agg := r.RunStepStart(context.Background(), "sess-1", 3)
	require.Equal(t, "note one\nnote two", agg.Context)

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "StepStart", payload["event"])
	require.EqualValues(t, 3, payload["step"])
}

func TestRunStopBlockViaJSONAndExitCode(t *testing.T) {
	t.Parallel()

	viaJSON := runnerFor(t, EventStop, config.HookConfig{Command: `echo '{"decision":"block","reason":"keep going"}'`})
	agg := viaJSON.RunStop(context.Background(), "s", "final text", false)
	require.True(t, agg.ContinueRequested())
	require.Equal(t, "keep going", agg.Reason)

	viaExit := runnerFor(t, EventStop, config.HookConfig{Command: `echo "tests still failing" >&2; exit 2`})
	agg = viaExit.RunStop(context.Background(), "s", "final text", false)
	require.True(t, agg.ContinueRequested())
	require.Equal(t, "tests still failing", agg.Reason)

	quiet := runnerFor(t, EventStop, config.HookConfig{Command: `echo '{"decision":"approve"}'`})
	require.False(t, quiet.RunStop(context.Background(), "s", "final text", false).ContinueRequested())
}

func TestRunStopPayloadCarriesTextAndActiveFlag(t *testing.T) {
	out := filepath.Join(t.TempDir(), "payload.json")
	t.Setenv("CRUSH_STOP_HOOK_OUT", out)

	r := runnerFor(t, EventStop, config.HookConfig{Command: `cat > "$CRUSH_STOP_HOOK_OUT"`})
	r.RunStop(context.Background(), "sess-1", "all done", true)

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "Stop", payload["hook_event_name"])
	require.Equal(t, "all done", payload["text"])
	require.Equal(t, true, payload["stop_hook_active"])
}

func TestNewEventsAreNormalizedAndListed(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"text_complete": "TextComplete",
		"TEXTCOMPLETE":  "TextComplete",
		"step_start":    "StepStart",
		"stop":          "Stop",
	} {
		require.Equal(t, want, NormalizeEventName(in), in)
	}
	require.Contains(t, AllEvents, EventTextComplete)
	require.Contains(t, AllEvents, EventStepStart)
	require.Contains(t, AllEvents, EventStop)
	require.False(t, IsToolEvent(EventTextComplete))
}
