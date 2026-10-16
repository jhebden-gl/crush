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

func TestStopEventIsRegistered(t *testing.T) {
	t.Parallel()
	requireEventRegistered(t, EventStop, "stop")
	requireEventRegistered(t, EventStop, "STOP")
}

func TestParseStdoutBlockDecision(t *testing.T) {
	t.Parallel()

	got := parseStdout(`{"decision":"block","reason":"work remains"}`)
	require.True(t, got.Block)
	require.Equal(t, "work remains", got.Reason)
	// Block is a separate outcome from deny; a Stop hook asks for another pass.
	require.Equal(t, DecisionNone, got.Decision)

	require.True(t, parseStdout(`{"decision":"BLOCK"}`).Block, "case-insensitive")
	require.False(t, parseStdout(`{"decision":"approve"}`).Block)
	require.False(t, parseStdout(`{"decision":"allow"}`).Block)
	require.False(t, parseStdout(`{}`).Block)
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

func TestContinueRequested(t *testing.T) {
	t.Parallel()

	require.True(t, AggregateResult{Block: true}.ContinueRequested())
	require.True(t, AggregateResult{Decision: DecisionDeny}.ContinueRequested())
	// A halt ends the turn, even though exit code 49 also reads as a deny.
	require.False(t, AggregateResult{Decision: DecisionDeny, Halt: true}.ContinueRequested())
	require.False(t, AggregateResult{Block: true, Halt: true}.ContinueRequested())
	require.False(t, AggregateResult{Decision: DecisionAllow}.ContinueRequested())
	require.False(t, AggregateResult{Decision: DecisionNone}.ContinueRequested())
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

	failing := runnerFor(t, EventStop, config.HookConfig{Command: `exit 1`})
	require.False(t, failing.RunStop(context.Background(), "s", "final text", false).ContinueRequested(),
		"a hook that crashes must not extend the turn")
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
	require.Equal(t, EventStop, payload["event"])
	require.Equal(t, EventStop, payload["hook_event_name"])
	require.Equal(t, "sess-1", payload["session_id"])
	require.Equal(t, "all done", payload["text"])
	require.Equal(t, true, payload["stop_hook_active"])
}

func TestRunStopWithoutHooksIsANoop(t *testing.T) {
	t.Parallel()
	r := runnerFor(t, EventStop)
	agg := r.RunStop(context.Background(), "s", "text", false)
	require.False(t, agg.ContinueRequested())
	require.Zero(t, agg.HookCount)
}

func TestRunStopHaltEndsTheTurnInsteadOfContinuing(t *testing.T) {
	t.Parallel()

	viaExit := runnerFor(t, EventStop, config.HookConfig{Command: `echo "stop everything" >&2; exit 49`})
	agg := viaExit.RunStop(context.Background(), "s", "text", false)
	require.True(t, agg.Halt)
	require.False(t, agg.ContinueRequested())

	viaJSON := runnerFor(t, EventStop, config.HookConfig{Command: `echo '{"decision":"deny","halt":true,"reason":"no"}'`})
	agg = viaJSON.RunStop(context.Background(), "s", "text", false)
	require.False(t, agg.ContinueRequested())
}
