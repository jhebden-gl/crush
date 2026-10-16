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

func TestStepStartEventIsRegistered(t *testing.T) {
	t.Parallel()
	requireEventRegistered(t, EventStepStart, "step_start")
	requireEventRegistered(t, EventStepStart, "STEPSTART")
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
	require.Equal(t, EventStepStart, payload["event"])
	require.Equal(t, EventStepStart, payload["hook_event_name"])
	require.Equal(t, "sess-1", payload["session_id"])
	require.EqualValues(t, 3, payload["step"])
}

func TestRunStepStartWithoutHooksIsANoop(t *testing.T) {
	t.Parallel()
	r := runnerFor(t, EventStepStart)
	agg := r.RunStepStart(context.Background(), "sess-1", 0)
	require.Equal(t, DecisionNone, agg.Decision)
	require.Empty(t, agg.Context)
	require.Zero(t, agg.HookCount)
}

func TestRunStepStartFailingHookAddsNoContext(t *testing.T) {
	t.Parallel()
	r := runnerFor(t, EventStepStart, config.HookConfig{Command: `echo "nope" >&2; exit 1`})
	agg := r.RunStepStart(context.Background(), "sess-1", 0)
	require.Empty(t, agg.Context)
}
