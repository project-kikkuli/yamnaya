package claude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// peerProtocol is the version of Claude Code's cross-session messaging socket that ym speaks.
const peerProtocol = 1

// ErrNoInbox means the session advertises no messaging socket in a protocol ym speaks.
var ErrNoInbox = errors.New("no messaging socket ym can use")

// ErrHeld means the session would hold a message from ym for the owner's review instead of reading it.
var ErrHeld = errors.New("the session holds messages from ym for review")

// envelope is the tag Claude Code wraps cross-session messages in.
const envelope = "cross-session-message"

type inboxMessage struct {
	MsgV      int    `json:"msgV"`
	MsgID     string `json:"msg_id"`
	Type      string `json:"type"`
	Message   body   `json:"message"`
	Priority  string `json:"priority"`
	From      string `json:"from"`
	SessionID string `json:"session_id"`
}

// receipt is the status a session reports back to a sender's own socket for a message it did not queue at once.
type receipt struct {
	Type      string   `json:"type"`
	Action    string   `json:"action"`
	Status    string   `json:"status"`
	OrigMsgID string   `json:"orig_msg_id"`
	Dropped   []string `json:"dropped_msg_ids"`
}

// NotReadError is a session's receipt saying it held, refused or dropped a message instead of queueing it.
type NotReadError struct{ Status string }

func (e NotReadError) Error() string { return "the session reported the message " + e.Status }

type body struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Message queues text as the session's next prompt through its messaging socket, only where the inbound gate admits ym.
// It counts once the session read the frame, closed its side and sent no held, refused or dropped receipt in time.
func (a *Adapter) Message(s core.Session, text string) error { return a.message(s, text, true) }

func (a *Adapter) message(s core.Session, text string, gate bool) error {
	st, ok := a.state(s.PID)
	if !ok || st.SessionID != s.ID || st.PeerProtocol != peerProtocol || st.MessagingSocket == "" {
		return ErrNoInbox
	}
	if gate && !a.admits(st.Cwd, a.scan(a.transcript(st.Cwd, s.ID)).Mode) {
		return ErrHeld
	}
	back, err := listenReceipts(filepath.Dir(st.MessagingSocket))
	if err != nil {
		return err
	}
	defer back.Close()
	var frame bytes.Buffer
	if token := a.peerToken(s.PID, st.MessagingSocket); token != "" {
		line, _ := json.Marshal(map[string]string{"type": "auth", "token": token})
		frame.Write(append(line, '\n'))
	}
	id := uuid.NewString()
	line, err := json.Marshal(inboxMessage{
		MsgV:      peerProtocol,
		MsgID:     id,
		Type:      "user",
		Message:   body{Role: "user", Content: wrap(text)},
		Priority:  "next",
		From:      "uds:" + back.Addr().String(),
		SessionID: s.ID,
	})
	if err != nil {
		return err
	}
	frame.Write(append(line, '\n'))
	timeout := a.SendTimeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	if err := deliver(st.MessagingSocket, frame.Bytes(), timeout); err != nil {
		return err
	}
	wait := a.ReceiptWait
	if wait == 0 {
		wait = time.Second
	}
	return awaitReceipt(back, id, wait)
}

// listenReceipts opens a socket for receipts beside the session's own, where Claude Code agrees to send them.
func listenReceipts(dir string) (*net.UnixListener, error) {
	return net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "ym-"+strconv.Itoa(os.Getpid())+"-"+uuid.NewString()[:8]+".sock"), Net: "unix"})
}

// awaitReceipt reads receipts until wait passes; one saying the session did not queue message id is the error.
func awaitReceipt(ln *net.UnixListener, id string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		if err := ln.SetDeadline(deadline); err != nil {
			return err
		}
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return nil
			}
			return err
		}
		conn.SetDeadline(deadline)
		dec := json.NewDecoder(conn)
		for {
			var r receipt
			if dec.Decode(&r) != nil {
				break
			}
			if r.Type != "control" || r.Action != "peer_message_status" || (r.OrigMsgID != id && !slices.Contains(r.Dropped, id)) {
				continue
			}
			if r.Status != "delivered" {
				conn.Close()
				return NotReadError{Status: r.Status}
			}
		}
		conn.Close()
	}
}

// inboundRank orders crossSessionInbound values; a repository's settings may only raise it.
var inboundRank = map[string]int{"accept": 0, "hold": 1, "refuse": 2}

// promptingModes are the permission modes Claude Code does not count as bypassing prompts.
var promptingModes = map[string]bool{"default": true, "acceptEdits": true, "auto": true, "dontAsk": true}

// admits mirrors Claude Code's inbound gate for a sender that attests no permission mode: the crossSessionInbound
// setting decides when set, else a session that bypasses permission prompts holds the message.
func (a *Adapter) admits(cwd, mode string) bool {
	policy, invalid := "", false
	read := func(path string) string {
		v, set := inbound(path)
		if _, known := inboundRank[v]; set && !known {
			invalid = true
			return ""
		}
		return v
	}
	for _, f := range []string{a.ManagedSettings, filepath.Join(a.Dir, "settings.json")} {
		if v := read(f); v != "" && policy == "" {
			policy = v
		}
	}
	if cwd != "" {
		for _, f := range []string{filepath.Join(cwd, ".claude", "settings.local.json"), filepath.Join(cwd, ".claude", "settings.json")} {
			if v := read(f); v != "" && inboundRank[v] > inboundRank[policy] {
				policy = v
			}
		}
	}
	if invalid && inboundRank[policy] < inboundRank["hold"] {
		policy = "hold"
	}
	if policy != "" {
		return policy == "accept"
	}
	return promptingModes[mode]
}

// inbound reads crossSessionInbound from a settings file; set is true when the key is present, whatever its value.
// Claude Code ignores a value it does not know and holds messages while any file carries one.
func inbound(path string) (value string, set bool) {
	if path == "" {
		return "", false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var file struct {
		CrossSessionInbound json.RawMessage `json:"crossSessionInbound"`
	}
	if json.Unmarshal(b, &file) != nil || file.CrossSessionInbound == nil || string(file.CrossSessionInbound) == "null" {
		return "", false
	}
	json.Unmarshal(file.CrossSessionInbound, &value)
	return value, true
}

// wrap puts text in the envelope Claude Code's own senders use, so the session shows it as a message from ym.
func wrap(text string) string {
	text = strings.ReplaceAll(text, "<"+envelope, `<\`+envelope)
	text = strings.ReplaceAll(text, "</"+envelope, `<\/`+envelope)
	return fmt.Sprintf("<%s from-name=\"ym\">\n%s\n</%s>", envelope, text, envelope)
}

// peerToken is the auth token Claude Code publishes beside its pid file for its socket, "" when there is none.
func (a *Adapter) peerToken(pid int, sock string) string {
	abs, err := filepath.Abs(sock)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(abs))
	b, err := os.ReadFile(filepath.Join(a.Dir, "sessions", strconv.Itoa(pid)+"."+hex.EncodeToString(sum[:])+".key"))
	if err != nil {
		return ""
	}
	var key struct {
		PeerToken string `json:"peerToken"`
	}
	if json.Unmarshal(b, &key) != nil {
		return ""
	}
	return key.PeerToken
}

// deliver writes frame to the socket, closes the write side and waits for the peer to close its own after reading it.
func deliver(sock string, frame []byte, timeout time.Duration) error {
	conn, err := net.DialTimeout("unix", sock, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	if _, err := conn.Write(frame); err != nil {
		return err
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		return err
	}
	n, err := io.Copy(io.Discard, conn)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%s replied %d bytes; ym does not know this messaging protocol", sock, n)
	}
	return nil
}
