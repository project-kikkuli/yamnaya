package cli

import (
	"bytes"
	"encoding/json"
	"io"
)

type agentOutput struct {
	Log    io.Writer
	Thread func(string) error
	Err    error
	line   []byte
	tail   []byte
}

func (o *agentOutput) Write(p []byte) (int, error) {
	n, err := o.Log.Write(p)
	if err != nil {
		return n, err
	}
	o.tail = append(o.tail, p...)
	if len(o.tail) > 1<<20 {
		o.tail = append([]byte(nil), o.tail[len(o.tail)-(1<<20):]...)
	}
	if o.Thread != nil {
		o.line = append(o.line, p...)
		for {
			end := bytes.IndexByte(o.line, '\n')
			if end < 0 {
				break
			}
			line := o.line[:end]
			o.line = o.line[end+1:]
			var event struct {
				Type   string `json:"type"`
				Thread string `json:"thread_id"`
			}
			if json.Unmarshal(line, &event) == nil && event.Type == "thread.started" && event.Thread != "" {
				o.Err = o.Thread(event.Thread)
				o.Thread = nil
				o.line = nil
				break
			}
		}
	}
	return len(p), nil
}
