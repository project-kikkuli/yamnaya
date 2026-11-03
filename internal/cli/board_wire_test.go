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

func TestBoardReadRecoversFromAFrameItCannotUse(t *testing.T) {
	dir := t.TempDir()
	ssh := filepath.Join(dir, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nprintf 'ym-board1 full 00\\nnot gzip'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := newRemoteHosts([]string{"box"}, 0, nil)
	r.ssh, r.timeout = ssh, 5*time.Second
	r.held["box"] = heldBoard{raw: []byte(fakeBoard), token: boardToken([]byte(fakeBoard))}
	done := make(chan struct{})
	r.read("box", done)
	<-done
	if b := r.last["box"]; b.Err == nil {
		t.Fatal("a broken frame read as a board")
	}
	if _, held := r.held["box"]; held {
		t.Error("kept the snapshot the broken frame was against; the next read should ask for the whole board")
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
