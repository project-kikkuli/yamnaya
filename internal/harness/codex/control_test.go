package codex

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestSupervisorUsesNativeToolOutputAndRequiresIdleThread(t *testing.T) {
	for _, tc := range []struct {
		name, status            string
		reject, disconnect      bool
		wantTurn, wantUncertain bool
	}{
		{name: "idle", status: "idle", wantTurn: true},
		{name: "busy", status: "active"},
		{name: "unloaded", status: "notLoaded"},
		{name: "rejected", status: "idle", reject: true, wantTurn: true},
		{name: "lost receipt", status: "idle", disconnect: true, wantTurn: true, wantUncertain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "ym-rpc-test-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "s")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			received := make(chan []json.RawMessage, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				scanner := bufio.NewScanner(conn)
				var turns []json.RawMessage
				defer func() { received <- turns }()
				for scanner.Scan() {
					var req struct {
						ID     int             `json:"id"`
						Method string          `json:"method"`
						Params json.RawMessage `json:"params"`
					}
					if json.Unmarshal(scanner.Bytes(), &req) != nil {
						return
					}
					var result any = struct{}{}
					switch req.Method {
					case "initialized":
						continue
					case "initialize":
					case "thread/read":
						result = map[string]any{"thread": map[string]any{"id": "thread", "status": map[string]string{"type": tc.status}}}
					case "turn/start":
						turns = append(turns, req.Params)
						if tc.disconnect {
							return
						}
						if tc.reject {
							_ = json.NewEncoder(conn).Encode(map[string]any{"id": req.ID, "error": map[string]any{"code": -32602, "message": "unsupported tool output"}})
							continue
						}
						result = map[string]any{"turn": map[string]string{"id": "turn"}}
					}
					if json.NewEncoder(conn).Encode(map[string]any{"id": req.ID, "result": result}) != nil {
						return
					}
				}
			}()
			client, err := ConnectControl(path)
			if err != nil {
				t.Fatal(err)
			}
			err = client.Supervisor("thread", "Review the landed result")
			client.Close()
			turns := <-received
			if (len(turns) > 0) != tc.wantTurn {
				t.Fatalf("turn count %d", len(turns))
			}
			if errors.Is(err, core.ErrSubmissionUncertain) != tc.wantUncertain {
				t.Fatalf("uncertainty: %v", err)
			}
			if tc.name == "idle" && err != nil {
				t.Fatal(err)
			}
			if tc.name != "idle" && err == nil {
				t.Fatal("expected refusal")
			}
			if len(turns) == 0 {
				return
			}
			var params struct {
				Input      []json.RawMessage `json:"input"`
				ToolOutput struct {
					Namespace, Name, Output string
					Retain                  bool
				} `json:"toolOutput"`
				TurnTrigger string `json:"turnTrigger"`
			}
			if err := json.Unmarshal(turns[0], &params); err != nil {
				t.Fatal(err)
			}
			if params.Input == nil || len(params.Input) != 0 || params.ToolOutput.Namespace != "yamnaya" || params.ToolOutput.Name != "supervisor" || params.ToolOutput.Output != "Review the landed result" || !params.ToolOutput.Retain || params.TurnTrigger != "supervisor" {
				t.Fatalf("wrong native action: %s", turns[0])
			}
		})
	}
}

func TestControlConnectionRejectsNonPrivateAndSymlinkEndpoints(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ym-rpc-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := ConnectControl(path); !errors.Is(err, ErrNoControl) {
		t.Fatalf("public endpoint admitted: %v", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ConnectControl(link); !errors.Is(err, ErrNoControl) {
		t.Fatalf("symlink admitted: %v", err)
	}
}
