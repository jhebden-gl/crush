package app

import (
	"bytes"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func assistantMsg(id, text string, finished bool) message.Message {
	m := message.Message{ID: id, Role: message.Assistant}
	m.SetContent(text)
	if finished {
		m.AddFinish(message.FinishReasonEndTurn, "", "")
	}
	return m
}

func TestStreamPrinterStreamsDeltasAsTheyGrow(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	p := newStreamPrinter(&out, false)
	require.NoError(t, p.handle(assistantMsg("m1", "  Hello", false)))
	require.NoError(t, p.handle(assistantMsg("m1", "  Hello world", false)))
	require.Equal(t, "Hello world", out.String())
}

func TestStreamPrinterHoldsOutputUntilTheMessageFinishes(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	p := newStreamPrinter(&out, true)
	require.NoError(t, p.handle(assistantMsg("m1", "we utilise it", false)))
	require.Empty(t, out.String(), "nothing is printed while a hook may still rewrite the text")

	// The rewrite shortened the text; printing only the final form means the
	// shrink never reaches stdout and never trips the length check.
	require.NoError(t, p.handle(assistantMsg("m1", "we use it", true)))
	require.Equal(t, "we use it", out.String())
}

func TestStreamPrinterHoldKeepsMessagesSeparate(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	p := newStreamPrinter(&out, true)
	require.NoError(t, p.handle(assistantMsg("m1", "one", true)))
	// Leading whitespace on a message's first print is trimmed, as it was
	// before the printer was extracted.
	require.NoError(t, p.handle(assistantMsg("m2", " two", true)))
	require.Equal(t, "onetwo", out.String())
}

func TestStreamPrinterStreamingModeStillRejectsShrinkingText(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	p := newStreamPrinter(&out, false)
	require.NoError(t, p.handle(assistantMsg("m1", "long text", false)))
	require.Error(t, p.handle(assistantMsg("m1", "short", false)))
}

func TestStreamPrinterIgnoresNonAssistantAndEmptyMessages(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	p := newStreamPrinter(&out, false)
	user := message.Message{ID: "u", Role: message.User}
	user.SetContent("question")
	require.NoError(t, p.handle(user))
	require.NoError(t, p.handle(message.Message{ID: "m1", Role: message.Assistant}))
	require.NoError(t, p.handle(assistantMsg("m2", "   ", false)))
	require.Empty(t, out.String())
}
