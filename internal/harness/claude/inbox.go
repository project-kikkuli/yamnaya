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
	"regexp"
	"slices"
	"strconv"
	"syscall"
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

// NotReadError is a session's receipt saying it held, refused, expired or dropped a message instead of queueing it.
type NotReadError struct{ Status string }

func (e NotReadError) Error() string { return "the session reported the message " + e.Status }

func (e NotReadError) Is(target error) bool {
	return target == core.ErrPendingApproval && e.Status == "held"
}

type body struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

var supervisorAction = regexp.MustCompile(`(?m)^Yamnaya supervisor action [a-f0-9]{32} \(attempt [1-9][0-9]*, `)

// Message sends genuine owner text or durable YM actions through the native peer socket.
// Automated peer delivery awaits the receiving session's explicit action acknowledgement.
func (a *Adapter) Message(s core.Session, text string, owner bool) error {
	if !owner && !supervisorAction.MatchString(text) {
		return errors.New("native Claude supervisor delivery requires a durable action identity and attempt")
	}
	st, ok := a.state(s.PID)
	if !ok || st.SessionID != s.ID || st.PeerProtocol != peerProtocol || st.MessagingSocket == "" {
		return ErrNoInbox
	}
	content := wrapFrom(text, "YM", "")
	if owner {
		mode := a.scan(a.transcript(st.Cwd, s.ID)).Mode
		if !a.admits(st.Cwd, mode, true) {
			return ErrHeld
		}
		content = wrap(text, attested(mode, true))
	}
	back, err := listenReceipts(filepath.Dir(st.MessagingSocket))
	if err != nil {
		return err
	}
	defer back.Close()
	var frame bytes.Buffer
	if owner {
		if token := a.peerToken(s.PID, st.MessagingSocket); token != "" {
			line, _ := json.Marshal(map[string]string{"type": "auth", "token": token})
			frame.Write(append(line, '\n'))
		}
	}
	id := uuid.NewString()
	line, err := json.Marshal(inboxMessage{
		MsgV:      peerProtocol,
		MsgID:     id,
		Type:      "user",
		Message:   body{Role: "user", Content: content},
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
	err = awaitReceipt(back, id, wait)
	if !owner {
		var notRead NotReadError
		if errors.As(err, &notRead) && notRead.Status != "held" {
			return err
		}
		if err != nil {
			if errors.Is(err, core.ErrSubmissionUncertain) {
				return err
			}
			return fmt.Errorf("%w: %w", core.ErrSubmissionUncertain, err)
		}
		return fmt.Errorf("%w: native Claude peer delivery awaits the receiving session's action acknowledgement", core.ErrSubmissionUncertain)
	}
	return err
}

// listenReceipts opens a socket for receipts beside the session's own, where Claude Code agrees to send them.
func listenReceipts(dir string) (*net.UnixListener, error) {
	sweepReceipts(dir)
	return net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, strconv.Itoa(os.Getpid())+"-"+uuid.NewString()[:8]+".sock"), Net: "unix"})
}

// sweepReceipts removes the receipt sockets of ym processes that ended by a signal before closing them.
func sweepReceipts(dir string) {
	socks, _ := filepath.Glob(filepath.Join(dir, "ym-*-*.sock"))
	for _, sock := range socks {
		m := receiptName.FindStringSubmatch(filepath.Base(sock))
		if m == nil {
			continue
		}
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && errors.Is(syscall.Kill(n, 0), syscall.ESRCH) {
			os.Remove(sock)
		}
	}
}

// receiptName recognizes legacy YM receipt sockets without matching native inboxes.
var receiptName = regexp.MustCompile(`^ym-([1-9][0-9]*)-[0-9a-f]{8}\.sock$`)

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
			switch r.Status {
			case "held", "refused", "expired", "dropped":
				conn.Close()
				return NotReadError{Status: r.Status}
			case "delivered":
			default:
				conn.Close()
				return fmt.Errorf("%w: unknown native peer receipt %q", core.ErrSubmissionUncertain, r.Status)
			}
		}
		conn.Close()
	}
}

// inboundRank orders crossSessionInbound values; a repository's settings may only raise it.
var inboundRank = map[string]int{"accept": 0, "hold": 1, "refuse": 2}

// promptingModes are the permission modes Claude Code does not count as bypassing prompts.
var promptingModes = map[string]bool{"default": true, "acceptEdits": true, "auto": true, "dontAsk": true}

// admits mirrors Claude Code's inbound gate for ym's sends: the crossSessionInbound setting decides when set,
// else the session reads it while it prompts for permissions, or bypasses them and ym attests the owner's own words.
func (a *Adapter) admits(cwd, mode string, owner bool) bool {
	invalid := false
	check := func(v string, set bool) string {
		if _, known := inboundRank[v]; set && !known {
			invalid = true
			return ""
		}
		return v
	}
	read := func(path string) string { return check(inbound(path)) }
	policy := check(a.managedInbound())
	if v := read(filepath.Join(a.Dir, "settings.json")); policy == "" {
		policy = v
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
	return promptingModes[mode] || attested(mode, owner) != ""
}

// attested is the permission mode ym claims for the owner's own words: bypass to a session that bypasses prompts,
// which reads a message only from a sender in its own mode, and none to the rest or for anyone else's words.
func attested(mode string, owner bool) string {
	if owner && mode == "bypassPermissions" {
		return "bypass"
	}
	return ""
}

// managedInbound is crossSessionInbound as the managed settings file and its drop-ins merge it, the last file setting it winning.
func (a *Adapter) managedInbound() (value string, set bool) {
	if a.ManagedSettings == "" {
		return "", false
	}
	drops, _ := filepath.Glob(filepath.Join(filepath.Dir(a.ManagedSettings), "managed-settings.d", "*.json"))
	for _, f := range append([]string{a.ManagedSettings}, drops...) {
		if v, ok := inbound(f); ok {
			value, set = v, true
		}
	}
	return value, set
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
func wrap(text, mode string) string {
	return wrapFrom(text, "ym", mode)
}

func wrapFrom(text, name, mode string) string {
	text = escapeClose(text)
	attrs := ` from-name="` + name + `"`
	if mode != "" {
		attrs += ` from-mode="` + mode + `"`
	}
	return fmt.Sprintf("<%s%s>\n%s\n</%s>", envelope, attrs, text, envelope)
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
		return fmt.Errorf("%w: %v", core.ErrSubmissionUncertain, err)
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		return fmt.Errorf("%w: %v", core.ErrSubmissionUncertain, err)
	}
	n, err := io.Copy(io.Discard, conn)
	if err != nil {
		return fmt.Errorf("%w: %v", core.ErrSubmissionUncertain, err)
	}
	if n > 0 {
		return fmt.Errorf("%w: %s replied %d bytes; ym does not know this messaging protocol", core.ErrSubmissionUncertain, sock, n)
	}
	return nil
}
