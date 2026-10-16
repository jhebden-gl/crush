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

func TestTextCompleteEventIsRegistered(t *testing.T) {
	t.Parallel()
	requireEventRegistered(t, EventTextComplete, "text_complete")
	requireEventRegistered(t, EventTextComplete, "TEXTCOMPLETE")
}

func TestParseStdoutUpdatedText(t *testing.T) {
	t.Parallel()

	got := parseStdout(`{"updated_text":"cleaned text"}`)
	require.Equal(t, "cleaned text", got.UpdatedText)
	require.Equal(t, DecisionNone, got.Decision)

	// A non-string value is ignored rather than coerced.
	require.Empty(t, parseStdout(`{"updated_text":{"a":1}}`).UpdatedText)
	require.Empty(t, parseStdout(`{"updated_text":null}`).UpdatedText)
	require.Empty(t, parseStdout(`{"updated_text":""}`).UpdatedText)
	require.Empty(t, parseStdout(`{}`).UpdatedText)
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
	require.Equal(t, EventTextComplete, payload["event"])
	require.Equal(t, EventTextComplete, payload["hook_event_name"])
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
