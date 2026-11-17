package codex

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

const ControlSocketEnv = "YAMNAYA_CONTROL_SOCKET"

var ErrNoControl = errors.New("session has no supported supervisor control connection")

type ControlClient struct {
	conn net.Conn
	read *bufio.Reader
	next int
}

func ConnectControl(path string) (*ControlClient, error) {
	if path == "" {
		return nil, ErrNoControl
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: socket unavailable or not private", ErrNoControl)
	}
	conn, err := net.DialTimeout("unix", path, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("supervisor connection: %w", err)
	}
	c := &ControlClient{conn: conn, read: bufio.NewReaderSize(conn, 64*1024)}
	if err := c.Call("initialize", struct {
		ClientInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}{ClientInfo: struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}{Name: "yamnaya", Version: "1"}}, nil); err != nil {
		conn.Close()
		return nil, err
	}
	if err := json.NewEncoder(conn).Encode(struct {
		Method string `json:"method"`
	}{Method: "initialized"}); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

type ControlRejection struct {
	Method  string
	Code    int
	Message string
}

func (e *ControlRejection) Error() string {
	return fmt.Sprintf("supervisor %s (%d): %s", e.Method, e.Code, e.Message)
}

func (c *ControlClient) Close() error { return c.conn.Close() }

func (c *ControlClient) Call(method string, params, result any) error {
	c.next++
	if err := c.conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	if err := json.NewEncoder(c.conn).Encode(struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{c.next, method, params}); err != nil {
		return err
	}
	for {
		line, err := c.readLine()
		if err != nil {
			return err
		}
		if len(line) > 16*1024*1024 {
			return errors.New("supervisor response exceeds limit")
		}
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(line, &response); err != nil {
			return fmt.Errorf("supervisor response: %w", err)
		}
		if response.ID != c.next {
			continue
		}
		if response.Error != nil {
			return &ControlRejection{Method: method, Code: response.Error.Code, Message: response.Error.Message}
		}
		if len(response.Result) == 0 {
			return errors.New("supervisor response has no result")
		}
		if result != nil {
			return json.Unmarshal(response.Result, result)
		}
		return nil
	}
}

type ControlThread struct {
	Thread struct {
		ID     string `json:"id"`
		Status struct {
			Type string `json:"type"`
		} `json:"status"`
	} `json:"thread"`
}

func (c *ControlClient) Supervisor(thread, text string) error {
	if thread == "" || strings.TrimSpace(text) == "" {
		return errors.New("supervisor action needs a thread and non-empty action")
	}
	var state ControlThread
	if err := c.Call("thread/read", struct {
		ThreadID string `json:"threadId"`
	}{thread}, &state); err != nil {
		return err
	}
	if state.Thread.ID != thread || state.Thread.Status.Type != "idle" {
		return fmt.Errorf("supervisor action waits: thread %s is %s", thread, state.Thread.Status.Type)
	}
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	err := c.Call("turn/start", struct {
		ThreadID    string   `json:"threadId"`
		Input       []string `json:"input"`
		TurnTrigger string   `json:"turnTrigger"`
		ToolOutput  struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
			Output    string `json:"output"`
			Retain    bool   `json:"retain"`
		} `json:"toolOutput"`
	}{ThreadID: thread, Input: []string{}, TurnTrigger: "supervisor", ToolOutput: struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Output    string `json:"output"`
		Retain    bool   `json:"retain"`
	}{Namespace: "yamnaya", Name: "supervisor", Output: text, Retain: true}}, &turn)
	if err != nil {
		var rejected *ControlRejection
		if errors.As(err, &rejected) {
			return err
		}
		return fmt.Errorf("%w: %v", core.ErrSubmissionUncertain, err)
	}
	if turn.Turn.ID == "" {
		return fmt.Errorf("%w: no accepted turn identity", core.ErrSubmissionUncertain)
	}
	return nil
}

func (a *Adapter) Message(s core.Session, text string, owner bool) error {
	if owner {
		return errors.New("native owner input uses the interactive session")
	}
	path := proc.ConfigRoot(s.PID, ControlSocketEnv)
	if a.ControlSocket != nil {
		path = a.ControlSocket(s)
	}
	c, err := ConnectControl(path)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Supervisor(s.ID, text)
}

func (c *ControlClient) readLine() ([]byte, error) {
	var line []byte
	for {
		part, err := c.read.ReadSlice('\n')
		if len(line)+len(part) > 16*1024*1024 {
			return nil, errors.New("supervisor response exceeds limit")
		}
		line = append(line, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}
