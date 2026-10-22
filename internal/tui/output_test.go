package tui

import (
	"bytes"
	"io"
	"os"
	"testing"
	"time"
)

func TestOutputNeverHoldsUpTheWriterAndKeepsOrder(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		t.Run(map[bool]string{false: "reading terminal", true: "stalled terminal"}[stalled], func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			o := newOutput(w)
			resume := make(chan struct{})
			got := make(chan []byte)
			go func() {
				if stalled {
					<-resume
				}
				b, _ := io.ReadAll(r)
				got <- b
			}()
			var want bytes.Buffer
			wrote := make(chan struct{})
			go func() {
				for i := range 64 {
					frame := bytes.Repeat([]byte{byte('a' + i%26)}, 64<<10)
					want.Write(frame)
					_, _ = o.Write(frame)
				}
				close(wrote)
			}()
			select {
			case <-wrote:
			case <-time.After(30 * time.Second):
				t.Fatal("writes waited on the terminal")
			}
			if stalled && !o.behind() {
				t.Fatal("not behind while the terminal reads nothing")
			}
			close(resume)
			o.drain()
			if o.behind() {
				t.Fatal("behind after draining")
			}
			w.Close()
			if b := <-got; !bytes.Equal(b, want.Bytes()) {
				t.Fatalf("terminal got %d bytes out of order or short of %d", len(b), want.Len())
			}
		})
	}
}
