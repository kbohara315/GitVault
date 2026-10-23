package app

import (
	"context"
	"errors"
	"testing"
)

func TestLocalWorkflowSubmitIsBoundedAndRejectsUnsupportedCommands(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &LocalWorkflow{ctx: ctx, commands: make(chan Command, 1)}
	if err := w.Submit(Command{Kind: Refresh}); err != nil {
		t.Fatal(err)
	}
	if err := w.Submit(Command{Kind: Refresh}); !errors.Is(err, ErrCommandQueueFull) {
		t.Fatalf("full command queue did not fail promptly: %v", err)
	}
	if err := w.Submit(Command{Kind: SaveToken, Token: "synthetic-sentinel"}); !errors.Is(err, ErrUnsupportedCommand) {
		t.Fatalf("unsupported secret command accepted: %v", err)
	}
	if len(w.commands) != 1 {
		t.Fatalf("unsupported command entered queue: %d queued commands", len(w.commands))
	}
	cancel()
	if err := w.Submit(Command{Kind: Refresh}); !errors.Is(err, ErrWorkflowClosed) {
		t.Fatalf("closed workflow accepted command: %v", err)
	}
}
