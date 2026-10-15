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
	SessionID string `json:"session_id"`
}

type body struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Message queues text as the next prompt of the session, through the messaging socket Claude Code keeps per process.
// The session reads it after its current turn, or at once when idle. Claude Code never replies on the socket and sends a
// receipt only for a message it holds, so ym sends only to a session that admits it and counts the delivery once the
// session read the whole frame and closed its side.
func (a *Adapter) Message(s core.Session, text string) error {
	st, ok := a.state(s.PID)
	if !ok || st.SessionID != s.ID || st.PeerProtocol != peerProtocol || st.MessagingSocket == "" {
		return ErrNoInbox
	}
	if !a.admits(st.Cwd, a.scan(a.transcript(st.Cwd, s.ID)).Mode) {
		return ErrHeld
	}
	var frame bytes.Buffer
	if token := a.peerToken(s.PID, st.MessagingSocket); token != "" {
		line, _ := json.Marshal(map[string]string{"type": "auth", "token": token})
		frame.Write(append(line, '\n'))
	}
	line, err := json.Marshal(inboxMessage{
		MsgV:      peerProtocol,
		MsgID:     uuid.NewString(),
		Type:      "user",
		Message:   body{Role: "user", Content: wrap(text)},
		Priority:  "next",
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
	return deliver(st.MessagingSocket, frame.Bytes(), timeout)
}

// inboundRank orders crossSessionInbound values; a repository's settings may only raise it.
var inboundRank = map[string]int{"accept": 0, "hold": 1, "refuse": 2}

// promptingModes are the permission modes Claude Code does not count as bypassing prompts.
var promptingModes = map[string]bool{"default": true, "acceptEdits": true, "auto": true, "dontAsk": true}

// admits mirrors Claude Code's inbound gate for a sender that attests no permission mode: the crossSessionInbound
// setting decides when set, else a session that bypasses permission prompts holds the message.
func (a *Adapter) admits(cwd, mode string) bool {
	policy := ""
	for _, f := range []string{a.ManagedSettings, filepath.Join(a.Dir, "settings.json")} {
		if v, ok := inbound(f); ok {
			policy = v
			break
		}
	}
	if cwd != "" {
		for _, f := range []string{filepath.Join(cwd, ".claude", "settings.local.json"), filepath.Join(cwd, ".claude", "settings.json")} {
			if v, ok := inbound(f); ok && inboundRank[v] > inboundRank[policy] {
				policy = v
			}
		}
	}
	if policy != "" {
		return policy == "accept"
	}
	return promptingModes[mode]
}

// inbound reads crossSessionInbound from a settings file; a value Claude Code does not know reads as hold, as Claude Code treats it.
func inbound(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var set struct {
		CrossSessionInbound *string `json:"crossSessionInbound"`
	}
	if json.Unmarshal(b, &set) != nil || set.CrossSessionInbound == nil {
		return "", false
	}
	if _, known := inboundRank[*set.CrossSessionInbound]; !known {
		return "hold", true
	}
	return *set.CrossSessionInbound, true
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
