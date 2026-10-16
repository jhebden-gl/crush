package app

import (
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/charmbracelet/crush/internal/message"
)

// streamPrinter writes assistant text to the output of a non-interactive run
// as message updates arrive.
//
// By default it streams: each update prints the text added since the last one.
// That assumes text only grows. A TextComplete hook can replace a finished
// block with shorter or different text, so when one is configured the printer
// holds each message back until it finishes and prints its final form once.
type streamPrinter struct {
	output   io.Writer
	hold     bool
	readSize map[string]int
	printed  bool
}

func newStreamPrinter(output io.Writer, hold bool) *streamPrinter {
	return &streamPrinter{output: output, hold: hold, readSize: map[string]int{}}
}

// handle prints whatever part of msg has not been printed yet.
func (p *streamPrinter) handle(msg message.Message) error {
	if msg.Role != message.Assistant || len(msg.Parts) == 0 {
		return nil
	}
	if p.hold && !msg.IsFinished() {
		return nil
	}

	content := msg.Content().String()
	readBytes := p.readSize[msg.ID]
	if len(content) < readBytes {
		slog.Error("Non-interactive: message content is shorter than read bytes", "message_length", len(content), "read_bytes", readBytes)
		return fmt.Errorf("message content is shorter than read bytes: %d < %d", len(content), readBytes)
	}

	part := content[readBytes:]
	// Trim leading whitespace. Sometimes the LLM includes leading
	// formatting and indentation, which we don't want here.
	if readBytes == 0 {
		part = strings.TrimLeft(part, " \t")
	}
	// Ignore initial whitespace-only messages.
	if p.printed || strings.TrimSpace(part) != "" {
		p.printed = true
		fmt.Fprint(p.output, part)
	}
	p.readSize[msg.ID] = len(content)
	return nil
}
