package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestBoardDeltaRebuildsTheTarget(t *testing.T) {
	var base bytes.Buffer
	for i := range 4000 {
		fmt.Fprintf(&base, `{"id":"session-%d","title":"work on item %d"},`, i, i*7)
	}
	b := base.Bytes()
	for _, c := range []struct {
		name         string
		base, target []byte
		under        int
	}{
		{"identical", b, b, 64},
		{"one byte changed", b, bytes.Replace(b, []byte("session-2000"), []byte("session-9000"), 1), 256},
		{"inserted", b, bytes.Replace(b, []byte(`"id":"session-3"`), []byte(`"id":"session-3","new":true`), 1), 256},
		{"blocks moved", b, append(append([]byte{}, b[len(b)/2:]...), b[:len(b)/2]...), 64},
		{"truncated", b, b[:len(b)/3], 64},
		{"empty target", b, nil, 16},
		{"empty base", nil, b, len(b) + 16},
		{"shorter than a block", []byte("{}"), []byte(`{"a":1}`), 16},
		{"nothing shared", b, bytes.Repeat([]byte("z"), 5000), 5016},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := boardDelta(c.base, c.target)
			if len(d) > c.under {
				t.Errorf("delta is %d bytes, want at most %d", len(d), c.under)
			}
			got, err := applyBoardDelta(c.base, d)
			if err != nil || !bytes.Equal(got, c.target) {
				t.Fatalf("rebuilt %d bytes, %v; want the %d byte target", len(got), err, len(c.target))
			}
		})
	}
}

func TestBrokenBoardDeltaIsRefused(t *testing.T) {
	for _, d := range [][]byte{{}, {200}, {1}, {0, 200, 1, 1}} {
		if _, err := applyBoardDelta([]byte("base"), d); err == nil {
			t.Errorf("delta %v rebuilt without error", d)
		}
	}
}

// hostFixture is a host's ssh, its ym and its state: the ssh runs the remote command in a shell and records how many bytes went out.
type hostFixture struct {
	ssh, state, sent string
}

func newHostFixture(t *testing.T, ym string) hostFixture {
	t.Helper()
	dir, state := t.TempDir(), t.TempDir()
	bin := filepath.Join(dir, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ym"), []byte("#!/bin/sh\n"+ym+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := hostFixture{ssh: filepath.Join(dir, "ssh"), state: state, sent: filepath.Join(dir, "sent")}
	script := `#!/bin/sh
for last; do :; done
HOME=` + dir + ` YAMNAYA_STATE_DIR=` + state + ` sh -c "$last" | tee ` + f.sent + `
`
	if err := os.WriteFile(f.ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f hostFixture) snapshot(t *testing.T, sessions int) {
	t.Helper()
	j := boardJSON{Self: "box", At: ts(time.Now())}
	for i := range sessions {
		j.Sessions = append(j.Sessions, sessionJSON{Harness: "claude", ID: fmt.Sprintf("session-%d", i), Title: strings.Repeat("title ", 20)})
	}
	raw, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath(f.state), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f hostFixture) sentBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(f.sent)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBoardReadsSendOnlyWhatChangedAndStillShowTheWholeBoard(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const wholeBoard = 5000
	for _, c := range []struct {
		name string
		ym   string
		// frames is what each read should travel as; empty means the whole board as plain JSON.
		frames [3]string
	}{
		{"this build", `` + testRun + `=1 YAMNAYA_NO_SERVE=1 exec ` + self + ` "$@"`, [3]string{"full", "same", "delta"}},
		{"build without --since", `case "$*" in *--since*) echo "unknown flag: --since" >&2; exit 1;; esac; cat "$YAMNAYA_STATE_DIR/board.json"`, [3]string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newHostFixture(t, c.ym)
			r := newRemoteHosts([]string{"box"}, 0, nil)
			r.ssh, r.timeout = f.ssh, 30*time.Second
			read := func(sessions int) core.RemoteBoard {
				t.Helper()
				done := make(chan struct{})
				r.read("box", done)
				<-done
				b := r.last["box"]
				if b.Err != nil || len(b.Sessions) != sessions {
					t.Fatalf("read %d sessions, err %v; want %d", len(b.Sessions), b.Err, sessions)
				}
				return b
			}
			check := func(step int) {
				t.Helper()
				sent := f.sentBytes(t)
				switch want := c.frames[step]; want {
				case "":
					if sent[0] != '{' {
						t.Errorf("read %d sent %.30q, want the plain board", step, sent)
					}
				case "same", "delta":
					if !bytes.HasPrefix(sent, []byte(boardFrame+want+" ")) || len(sent) > wholeBoard {
						t.Errorf("read %d sent %d bytes starting %.30q, want a small %s frame", step, len(sent), sent, want)
					}
				default:
					if !bytes.HasPrefix(sent, []byte(boardFrame+want+" ")) {
						t.Errorf("read %d sent %.30q, want a %s frame", step, sent, want)
					}
				}
			}
			f.snapshot(t, 200)
			read(200)
			check(0)
			first := read(200)
			check(1)
			if c.frames[1] == frameSame && &first.Sessions[0] != &r.last["box"].Sessions[0] {
				t.Error("an unchanged snapshot was parsed again")
			}
			f.snapshot(t, 201)
			read(201)
			check(2)
		})
	}
}

func TestBoardReadStartsFromTheBoardAnEarlierProcessKept(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := newHostFixture(t, testRun+`=1 YAMNAYA_NO_SERVE=1 exec `+self+` "$@"`)
	f.snapshot(t, 100)
	earlier := newRemoteHosts([]string{"box"}, 0, nil)
	earlier.ssh, earlier.timeout = f.ssh, 30*time.Second
	done := make(chan struct{})
	earlier.read("box", done)
	<-done
	kept := earlier.held["box"].raw

	later := newRemoteHosts([]string{"box"}, 0, nil)
	later.ssh, later.timeout = f.ssh, 30*time.Second
	later.stored = func(string) []byte { return kept }
	done = make(chan struct{})
	later.read("box", done)
	<-done
	if b := later.last["box"]; b.Err != nil || len(b.Sessions) != 100 {
		t.Fatalf("read %d sessions, err %v", len(b.Sessions), b.Err)
	}
	if sent := f.sentBytes(t); !bytes.HasPrefix(sent, []byte(boardFrame+frameSame+" ")) {
		t.Errorf("sent %.30q, want only that the kept board still stands", sent)
	}
}

// answeringSSH is an ssh that gives each read the next of answers, repeating the last, and logs the command it was asked to run.
func answeringSSH(t *testing.T, answers ...[]byte) (ssh, log string) {
	t.Helper()
	dir := t.TempDir()
	for i, answer := range answers {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("answer-%d", i)), answer, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ssh, log = filepath.Join(dir, "ssh"), filepath.Join(dir, "log")
	script := fmt.Sprintf(`#!/bin/sh
for last; do :; done
echo "$last" >> %[1]s
n=$(($(wc -l < %[1]s) - 1))
[ "$n" -gt %[2]d ] && n=%[2]d
cat %[3]s/answer-$n
`, log, len(answers)-1, dir)
	if err := os.WriteFile(ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return ssh, log
}

func readOnce(r *remoteHosts, host string) core.RemoteBoard {
	done := make(chan struct{})
	r.read(host, done)
	<-done
	return r.last[host]
}

func TestBoardReadAsksForTheWholeBoardAfterAFrameItCannotUse(t *testing.T) {
	kept := []byte(fakeBoard)
	ssh, log := answeringSSH(t, []byte("ym-board1 full 00\nnot gzip"), kept)
	r := newRemoteHosts([]string{"box"}, 0, nil)
	r.ssh, r.timeout = ssh, 5*time.Second
	r.stored = func(string) []byte { return kept }
	if b := readOnce(r, "box"); b.Err == nil {
		t.Fatal("a broken frame read as a board")
	}
	if b := readOnce(r, "box"); b.Err != nil || len(b.Sessions) != 1 {
		t.Fatalf("read after the broken frame: %d sessions, err %v", len(b.Sessions), b.Err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	commands := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(commands) != 2 || !strings.Contains(commands[0], boardToken(kept)) {
		t.Fatalf("commands %q, want the first to name the kept board", commands)
	}
	if strings.Contains(commands[1], boardToken(kept)) || !strings.Contains(commands[1], "--since "+shellQuote(noBoard)) {
		t.Errorf("second read ran %q, want it to ask for the whole board", commands[1])
	}
}

func TestPlainAnswerNeverStandsForTheSnapshotHeld(t *testing.T) {
	var held boardJSON
	if err := json.Unmarshal([]byte(fakeBoard), &held); err != nil {
		t.Fatal(err)
	}
	heldRaw, otherRaw := []byte(fakeBoard), []byte(`{"self":"other"}`)
	ssh, _ := answeringSSH(t, otherRaw, heldRaw)
	r := newRemoteHosts([]string{"box"}, 0, nil)
	r.ssh, r.timeout = ssh, 5*time.Second
	r.held["box"] = heldBoard{raw: heldRaw, token: boardToken(heldRaw)}
	if b := readOnce(r, "box"); b.Err != nil || b.Self != "other" {
		t.Fatalf("plain answer read as self %q, err %v", b.Self, b.Err)
	}
	if b := readOnce(r, "box"); b.Err != nil || len(b.Sessions) != len(held.Sessions) || b.Self == "other" {
		t.Fatalf("the held bytes read as self %q with %d sessions, err %v", b.Self, len(b.Sessions), b.Err)
	}
}

func TestBoardBaseIsRepairedWhenItsSnapshotIsSentAgain(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(`{"self":"box"}`)
	path := filepath.Join(dir, "board-bases", boardToken(raw))
	for _, step := range []string{"first send", "after damage"} {
		if err := writeBoardSince(&bytes.Buffer{}, dir, raw, noBoard); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("%s: base holds %q, %v; want %q", step, got, err, raw)
		}
		if err := os.WriteFile(path, []byte("damaged"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBoardBasesKeepOnlyTheNewest(t *testing.T) {
	dir := t.TempDir()
	for i := range boardBases + 4 {
		raw := []byte(fmt.Sprintf(`{"at":"%d"}`, i))
		if err := writeBoardSince(&bytes.Buffer{}, dir, raw, noBoard); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "board-bases", boardToken(raw))
		at := time.Now().Add(time.Duration(i-100) * time.Minute)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "board-bases"))
	if err != nil || len(entries) != boardBases {
		t.Fatalf("%d bases kept, %v; want %d", len(entries), err, boardBases)
	}
	if _, err := os.Stat(filepath.Join(dir, "board-bases", boardToken([]byte(fmt.Sprintf(`{"at":"%d"}`, boardBases+3))))); err != nil {
		t.Errorf("newest base gone: %v", err)
	}
}
