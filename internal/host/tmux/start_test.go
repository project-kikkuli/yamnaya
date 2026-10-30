package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStartReadsThePaneWhenTmuxCutsItsOwnReport(t *testing.T) {
	for _, c := range []struct {
		name, report string
	}{
		{"whole report", ""},
		{"report cut after the pane id", "%0\t"},
		{"no report", "-"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, conf := ymServer(t)
			real, _ := exec.LookPath("tmux")
			bin := t.TempDir()
			wrapper := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = new-session ] && cut=1; done\n" +
				"if [ -n \"$cut\" ] && [ -n \"$YM_TEST_REPORT\" ]; then " + real + " \"$@\" >/dev/null || exit; [ \"$YM_TEST_REPORT\" = - ] || printf '%s' \"$YM_TEST_REPORT\"; exit 0; fi\n" +
				"exec " + real + " \"$@\"\n"
			if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(wrapper), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("YM_TEST_REPORT", c.report)
			p, err := h.Start(conf, "repo-wt-x", t.TempDir(), []string{"sleep", "30"})
			if err != nil {
				t.Fatal(err)
			}
			if p.ID == "" || p.TTY == "" || p.PID == 0 || p.Dead {
				t.Fatalf("Start = %+v, want the live pane with its tty and pid", p)
			}
		})
	}
}

func TestStartRunsACommandLongerThanTmuxCarries(t *testing.T) {
	for _, size := range []int{10, 64 << 10, 512 << 10} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			h, conf := ymServer(t)
			dir := t.TempDir()
			out := filepath.Join(dir, "out")
			arg := strings.Repeat("it's \"long\" $HOME `x` ", size/20+1)[:size]
			if _, err := h.Start(conf, "repo-wt-long", dir, []string{"sh", "-c", `printf %s "$1" > "$2"; sleep 30`, "sh", arg, out}); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				if got, _ := os.ReadFile(out); string(got) == arg {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the started command never received its %d-byte argument intact", size)
				}
				time.Sleep(50 * time.Millisecond)
			}
			if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "ym-start-*.sh")); len(left) > 0 {
				for _, f := range left {
					if body, _ := os.ReadFile(f); strings.Contains(string(body), out) {
						t.Fatalf("start script %s left behind", f)
					}
				}
			}
		})
	}
}
