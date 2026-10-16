package hooks

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// runnerFor builds a Runner with hooks registered for a single event.
func runnerFor(t *testing.T, event string, hooks ...config.HookConfig) *Runner {
	t.Helper()
	return NewRunner(map[string][]config.HookConfig{event: hooks}, t.TempDir(), t.TempDir())
}

// requireEventRegistered checks that an event is listed, that its user
// spelling normalizes to the canonical name, and that the config loader
// accepts and renames it. The config package keeps its own copy of the event
// names, so this is what catches the two lists drifting apart.
func requireEventRegistered(t *testing.T, event, spelling string) {
	t.Helper()
	require.Equal(t, event, NormalizeEventName(spelling))
	require.Contains(t, AllEvents, event)
	require.False(t, IsToolEvent(event))

	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{spelling: {{Command: "true"}}},
	}
	require.NoError(t, cfg.ValidateHooks())
	require.Len(t, cfg.Hooks[event], 1)
	require.NotContains(t, cfg.Hooks, spelling)
}
