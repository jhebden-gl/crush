package message

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetContentReplacesTheTextPartInPlace(t *testing.T) {
	t.Parallel()

	m := Message{Parts: []ContentPart{
		ReasoningContent{Thinking: "hmm"},
		TextContent{Text: "old text"},
		ToolCall{ID: "t1", Name: "echo"},
	}}
	m.SetContent("new text")

	require.Equal(t, "new text", m.Content().Text)
	require.Len(t, m.Parts, 3)
	require.IsType(t, ReasoningContent{}, m.Parts[0])
	require.IsType(t, ToolCall{}, m.Parts[2])
}

func TestSetContentKeepsTheHiddenFlag(t *testing.T) {
	t.Parallel()

	m := Message{Parts: []ContentPart{TextContent{Text: "old", Hidden: true}}}
	m.SetContent("new")
	require.True(t, m.Content().Hidden)
}

func TestSetContentAddsATextPartWhenThereIsNone(t *testing.T) {
	t.Parallel()

	m := Message{}
	m.SetContent("fresh")
	require.Equal(t, "fresh", m.Content().Text)
	require.Len(t, m.Parts, 1)
}
