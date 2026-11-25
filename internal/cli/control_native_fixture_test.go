package cli

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

const codexFixtureBinary = "YM_CODEX_FIXTURE_BINARY"

func dispatchControlFixture() bool {
	if len(os.Args) < 2 {
		return false
	}
	if os.Getenv(codexFixtureBinary) != "" && (os.Args[1] == "app-server" || os.Args[1] == "--remote") {
		if err := runCodexNativeFixture(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Args[1] != "control" {
		return false
	}
	args := append([]string(nil), os.Args[1:]...)
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "--binary" {
			continue
		}
		binary := args[i+1]
		file, err := os.Open(binary)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		prefix := make([]byte, 64)
		n, _ := file.Read(prefix)
		file.Close()
		header, _, _ := strings.Cut(string(prefix[:n]), "\n")
		if header != "#!/bin/sh" && header != "#!/bin/bash" {
			fmt.Fprintln(os.Stderr, "native control fixture requires an explicit sh or bash fixture")
			os.Exit(1)
		}
		if len(args) > 1 && args[1] == "codex" {
			self, err := os.Executable()
			if err != nil {
				os.Exit(1)
			}
			os.Setenv(codexFixtureBinary, binary)
			args[i+1] = self
		}
		break
	}
	root := NewRoot()
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
	return true
}

func runCodexNativeFixture(args []string) error {
	original := os.Getenv(codexFixtureBinary)
	if original == "" {
		return errors.New("native fixture binary required")
	}
	if args[0] == "--remote" {
		return syscall.Exec(original, append([]string{original}, args...), os.Environ())
	}
	socket := ""
	sqliteRoot := os.Getenv("CODEX_SQLITE_HOME")
	if sqliteRoot == "" {
		sqliteRoot = os.Getenv("CODEX_HOME")
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--listen" {
			socket = strings.TrimPrefix(args[i+1], "unix://")
		}
		if args[i] == "-c" && strings.HasPrefix(args[i+1], "sqlite_home=") {
			value := strings.TrimPrefix(args[i+1], "sqlite_home=")
			if decoded, err := strconv.Unquote(value); err == nil {
				sqliteRoot = decoded
			} else {
				sqliteRoot = value
			}
		}
	}
	if socket == "" || sqliteRoot == "" {
		return errors.New("native fixture requires an isolated socket and SQLite root")
	}
	if err := os.MkdirAll(sqliteRoot, 0700); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(sqliteRoot, "state_5.sqlite")+"?"+url.Values{"_pragma": {"busy_timeout(5000)"}}.Encode())
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS threads(id TEXT PRIMARY KEY,rollout_path TEXT,created_at INTEGER,updated_at INTEGER,cwd TEXT,title TEXT,archived INTEGER DEFAULT 0,first_user_message TEXT DEFAULT '',name TEXT,git_branch TEXT,agent_role TEXT,agent_nickname TEXT,agent_path TEXT,model TEXT)`)
	if err != nil {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	var mutex sync.Mutex
	thread := ""
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer conn.Close()
			scanner := bufio.NewScanner(conn)
			scanner.Buffer(make([]byte, 4096), 4*1024*1024)
			for scanner.Scan() {
				raw := append([]byte(nil), scanner.Bytes()...)
				var request struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params struct {
						ThreadID   string                                    `json:"threadId"`
						Cwd        string                                    `json:"cwd"`
						Input      []json.RawMessage                         `json:"input"`
						ToolOutput *struct{ Namespace, Name, Output string } `json:"toolOutput"`
					} `json:"params"`
				}
				if json.Unmarshal(raw, &request) != nil {
					return
				}
				mutex.Lock()
				if path := os.Getenv("YM_CODEX_FIXTURE_EVENTS"); path != "" {
					file, e := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
					if e == nil {
						file.Write(append(raw, '\n'))
						file.Close()
					}
				}
				var result any = map[string]any{}
				switch request.Method {
				case "initialized":
					mutex.Unlock()
					continue
				case "initialize":
				case "thread/start":
					thread = uuid.NewString()
					if data, e := os.ReadFile(os.Getenv("YM_CODEX_FIXTURE_ID_FILE")); e == nil && strings.TrimSpace(string(data)) != "" {
						thread = strings.TrimSpace(string(data))
					}
					if os.Getenv("YM_CODEX_FIXTURE_DELAY_REGISTRATION") != "1" {
						_, err = db.Exec(`INSERT OR REPLACE INTO threads(id,rollout_path,created_at,updated_at,cwd,title) VALUES(?,'',?,?,?,'fixture')`, thread, time.Now().Unix(), time.Now().Unix(), request.Params.Cwd)
					}
					result = map[string]any{"thread": map[string]any{"id": thread, "status": map[string]string{"type": "idle"}}}
				case "thread/resume":
					thread = request.Params.ThreadID
					result = map[string]any{"thread": map[string]any{"id": thread, "status": map[string]string{"type": "idle"}}}
				case "thread/read":
					result = map[string]any{"thread": map[string]any{"id": request.Params.ThreadID, "status": map[string]string{"type": "idle"}}}
				case "turn/start":
					if request.Params.Input == nil || len(request.Params.Input) != 0 || request.Params.ToolOutput == nil {
						err = errors.New("native fixture only accepts non-user supervisor tool output")
					}
					result = map[string]any{"turn": map[string]string{"id": uuid.NewString()}}
				default:
					err = fmt.Errorf("unsupported native fixture method %s", request.Method)
				}
				response := map[string]any{"id": request.ID, "result": result}
				if err != nil {
					delete(response, "result")
					response["error"] = map[string]any{"code": -32602, "message": err.Error()}
					err = nil
				}
				mutex.Unlock()
				if json.NewEncoder(conn).Encode(response) != nil {
					return
				}
			}
		}()
	}
}

func TestManagedCodexInitializesNativeRuntimeAndUsesNoUserSupervisorTurn(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "codex-shell")
	capture := filepath.Join(dir, "tui-args")
	events := filepath.Join(dir, "events")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$YM_CAPTURE_ARGS\"\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(codexFixtureBinary, binary)
	t.Setenv("CODEX_HOME", filepath.Join(dir, "native-home"))
	t.Setenv("CODEX_SQLITE_HOME", "")
	t.Setenv("YM_CAPTURE_ARGS", capture)
	t.Setenv("YM_CODEX_FIXTURE_EVENTS", events)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := runCodexControl(context.Background(), self, "", "Review the completed result", "", []string{"--yolo"}); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(events)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	initialized, activated := false, false
	for scanner.Scan() {
		var request struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Fatal(err)
		}
		if request.Method == "initialize" {
			initialized = true
		}
		if request.Method == "turn/start" {
			activated = true
			var params struct {
				Input       []json.RawMessage                        `json:"input"`
				TurnTrigger string                                   `json:"turnTrigger"`
				ToolOutput  struct{ Namespace, Name, Output string } `json:"toolOutput"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.Input == nil || len(params.Input) != 0 || params.TurnTrigger != "supervisor" || params.ToolOutput.Namespace != "yamnaya" || params.ToolOutput.Name != "supervisor" || params.ToolOutput.Output != "Review the completed result" {
				t.Fatalf("wrong activation: %s", request.Params)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !initialized || !activated {
		t.Fatalf("native initialization/activation missing: %v %v", initialized, activated)
	}
	args, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), "Review the completed result") || !strings.Contains(string(args), "--remote") {
		t.Fatalf("supervisor action became TUI user input: %s", args)
	}
}

func TestManagedClaudeRunsTheExplicitBashNativeFixtureDirectly(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "claude-bash-fixture")
	capture := filepath.Join(dir, "native-args")
	script := "#!/bin/bash\nprintf '%s\\n' \"$@\" > \"$YM_CAPTURE_ARGS\"\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YM_CAPTURE_ARGS", capture)
	argv, err := managedClaudeArgv(launch.Record{Harness: "claude", Session: "native-session"}, []string{binary, "--resume", "native-session"})
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(argv[0], argv[1:]...)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("managed bash fixture never landed: %v %s", err, output)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(args) != 4 || args[0] != "--settings" || args[1] != `{"crossSessionInbound":"accept"}` || args[2] != "--resume" || args[3] != "native-session" {
		t.Fatalf("native launch lost scoped peer settings or exact resume identity: %q", args)
	}
}
