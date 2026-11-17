package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

const ChannelSocketEnv = "YAMNAYA_CLAUDE_CHANNEL_SOCKET"

var channelActionID = regexp.MustCompile(`(?m)^Yamnaya supervisor action ([a-f0-9]{32}) (?:\(attempt ([0-9]+), )?`)

type channelAction struct {
	Session string `json:"session"`
	ID      string `json:"id"`
	Attempt int    `json:"attempt,omitempty"`
	Content string `json:"content"`
}
type channelResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func (a *Adapter) channelMessage(s core.Session, text string) error {
	path := proc.ConfigRoot(s.PID, ChannelSocketEnv)
	if a.ChannelSocket != nil {
		path = a.ChannelSocket(s)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0077 != 0 {
		return ErrNoSupervisor
	}
	actions := channelActions(text)
	if len(actions) == 0 {
		return errors.New("Claude supervisor delivery requires a durable action identity")
	}
	conn, err := net.DialTimeout("unix", path, 3*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	if err = json.NewEncoder(conn).Encode(channelAction{Session: s.ID, ID: actions[0].ID, Attempt: actions[0].Attempt, Content: text}); err != nil {
		return fmt.Errorf("%w: %v", core.ErrSubmissionUncertain, err)
	}
	var reply channelResponse
	if err = json.NewDecoder(io.LimitReader(conn, 65536)).Decode(&reply); err != nil {
		return fmt.Errorf("%w: %v", core.ErrSubmissionUncertain, err)
	}
	if reply.Status == "refused" {
		return fmt.Errorf("Claude channel refused: %s", reply.Error)
	}
	return fmt.Errorf("%w: Claude channel notification awaits the accepting session's acknowledgement", core.ErrSubmissionUncertain)
}

type ChannelCallbacks struct {
	Accept   func(string) error
	Complete func(string, string) error
}

func ServeChannel(parent context.Context, socket, session string, in io.Reader, out io.Writer, callbacks ChannelCallbacks) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socket)
	if err = os.Chmod(socket, 0600); err != nil {
		return err
	}
	var mu, write sync.Mutex
	ready := false
	pending := map[string]channelAction{}
	submitted := map[channelActionRef]struct{}{}
	latest := map[string]int{}
	encode := func(v any) error { write.Lock(); defer write.Unlock(); return json.NewEncoder(out).Encode(v) }
	go func() { <-ctx.Done(); listener.Close() }()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				var action channelAction
				if err := json.NewDecoder(io.LimitReader(conn, 4*1024*1024)).Decode(&action); err != nil {
					return
				}
				actions := channelActions(action.Content)
				ids := make([]string, 0, len(actions))
				attempts := map[string]int{}
				for _, entry := range actions {
					if _, known := attempts[entry.ID]; !known {
						ids = append(ids, entry.ID)
					}
					if entry.Attempt >= attempts[entry.ID] {
						attempts[entry.ID] = entry.Attempt
					}
				}
				mu.Lock()
				valid := ready && action.Session == session && len(actions) > 0 && action.ID == actions[0].ID
				duplicate := true
				if valid {
					for _, ref := range actions {
						_, known := submitted[ref]
						submitted[ref] = struct{}{}
						if known || ref.Attempt < latest[ref.ID] {
							continue
						}
						entry := action
						entry.ID, entry.Attempt = ref.ID, ref.Attempt
						pending[ref.ID] = entry
						latest[ref.ID] = ref.Attempt
						duplicate = false
					}
				}
				mu.Unlock()
				reply := channelResponse{Status: "written"}
				if !valid {
					reply = channelResponse{Status: "refused", Error: "channel is not initialized or session/action identity does not match"}
				} else if !duplicate {
					notification := struct {
						JSONRPC string `json:"jsonrpc"`
						Method  string `json:"method"`
						Params  struct {
							Content string            `json:"content"`
							Meta    map[string]string `json:"meta"`
						} `json:"params"`
					}{JSONRPC: "2.0", Method: "notifications/claude/channel"}
					notification.Params.Content = action.Content
					attemptMeta, _ := json.Marshal(attempts)
					notification.Params.Meta = map[string]string{"action_id": ids[0], "action_ids": strings.Join(ids, ","), "action_attempts": string(attemptMeta), "session_id": session, "authority": "yamnaya_supervisor"}
					if err := encode(notification); err != nil {
						reply.Error = err.Error()
					}
				}
				_ = json.NewEncoder(conn).Encode(reply)
			}()
		}
	}()
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
				Name            string `json:"name"`
				Arguments       struct {
					ActionID string `json:"action_id"`
					Result   string `json:"result"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return err
		}
		if request.Method == "notifications/initialized" {
			mu.Lock()
			ready = true
			mu.Unlock()
			continue
		}
		if len(request.ID) == 0 {
			continue
		}
		var result any
		var rpcError error
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": request.Params.ProtocolVersion, "serverInfo": map[string]string{"name": "yamnaya", "version": "1"}, "capabilities": map[string]any{"tools": map[string]any{}, "experimental": map[string]any{"claude/channel": map[string]any{}}}, "instructions": "This channel carries Yamnaya supervisor actions, never messages from the human owner. Acknowledge an action ID before acting, then complete it with the actual result. Pending lists actions whose receipt is unconfirmed."}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			tool := func(name, description string, properties map[string]any, required []string) map[string]any {
				return map[string]any{"name": name, "description": description, "inputSchema": map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}
			}
			str := map[string]string{"type": "string"}
			result = map[string]any{"tools": []any{tool("pending", "Read submitted Yamnaya actions", map[string]any{}, []string{}), tool("acknowledge", "Confirm this session received a supervisor action", map[string]any{"action_id": str}, []string{"action_id"}), tool("complete", "Record what a supervisor action actually changed", map[string]any{"action_id": str, "result": str}, []string{"action_id", "result"})}}
		case "tools/call":
			id := request.Params.Arguments.ActionID
			mu.Lock()
			_, known := pending[id]
			mu.Unlock()
			content := ""
			switch request.Params.Name {
			case "pending":
				mu.Lock()
				actions := make([]channelAction, 0, len(pending))
				for _, a := range pending {
					actions = append(actions, a)
				}
				mu.Unlock()
				raw, _ := json.Marshal(actions)
				content = string(raw)
			case "acknowledge":
				if !known || callbacks.Accept == nil {
					rpcError = errors.New("action not submitted to this session")
				} else {
					rpcError = callbacks.Accept(id)
					content = "action received"
				}
			case "complete":
				if !known || callbacks.Complete == nil {
					rpcError = errors.New("action not submitted to this session")
				} else {
					rpcError = callbacks.Complete(id, request.Params.Arguments.Result)
					content = "result recorded"
					if rpcError == nil {
						mu.Lock()
						delete(pending, id)
						mu.Unlock()
					}
				}
			default:
				rpcError = errors.New("unknown channel tool")
			}
			if rpcError != nil {
				content = rpcError.Error()
			}
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": content}}, "isError": rpcError != nil}
			rpcError = nil
		default:
			rpcError = errors.New("unsupported MCP request")
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}
		if rpcError != nil {
			delete(response, "result")
			response["error"] = map[string]any{"code": -32601, "message": rpcError.Error()}
		}
		if err := encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

type channelActionRef struct {
	ID      string
	Attempt int
}

func channelActions(content string) []channelActionRef {
	matches := channelActionID.FindAllStringSubmatch(content, -1)
	actions := make([]channelActionRef, 0, len(matches))
	seen := map[channelActionRef]bool{}
	for _, match := range matches {
		ref := channelActionRef{ID: match[1]}
		if match[2] != "" {
			attempt, err := strconv.Atoi(match[2])
			if err != nil {
				continue
			}
			ref.Attempt = attempt
		}
		if !seen[ref] {
			seen[ref] = true
			actions = append(actions, ref)
		}
	}
	return actions
}
