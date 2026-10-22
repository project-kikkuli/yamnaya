package tui

import (
	"context"
	"os"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
)

func TestKeysFromTheTerminalArriveWithoutWaitingForMore(t *testing.T) {
	for _, c := range []struct{ sent, key string }{
		{"\x1b", "esc"},
		{"\x1b[A", "up"},
		{"j", "j"},
		{"\r", "enter"},
	} {
		t.Run(c.key, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			cr, err := uv.NewCancelReader(input{r})
			if err != nil {
				t.Fatal(err)
			}
			d := uv.NewTerminalReader(cr, "xterm-256color")
			d.EscTimeout = time.Hour
			events := make(chan uv.Event, 8)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = d.StreamEvents(ctx, events) }()
			if _, err := w.WriteString(c.sent); err != nil {
				t.Fatal(err)
			}
			select {
			case ev := <-events:
				if k, ok := ev.(uv.KeyPressEvent); !ok || k.String() != c.key {
					t.Fatalf("%q arrived as %#v, want %s", c.sent, ev, c.key)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%q is still waiting for the rest of a sequence", c.sent)
			}
			cr.Cancel()
		})
	}
}
