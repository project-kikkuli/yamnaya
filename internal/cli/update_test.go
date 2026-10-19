package cli

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestUpdateInstallsLatestAndReportsWhatLanded(t *testing.T) {
	for _, tc := range []struct {
		name     string
		env      string
		wantPath string
	}{
		{"GOBIN set", "/opt/gobin\n/home/u/go\n", "/opt/gobin/ym"},
		{"GOPATH only", "\n/home/u/go\n", "/home/u/go/bin/ym"},
		{"GOPATH list", "\n/home/u/go:/other\n", "/home/u/go/bin/ym"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			run := func(out io.Writer, args ...string) error {
				calls = append(calls, args)
				switch args[0] {
				case "install":
					fmt.Fprintln(out, "go: downloading github.com/project-kikkuli/yamnaya v0.0.0-20261019-abc")
				case "env":
					io.WriteString(out, tc.env)
				case "version":
					fmt.Fprintf(out, "%s: go1.26.0\n\tpath\tgithub.com/project-kikkuli/yamnaya/cmd/ym\n\tmod\tgithub.com/project-kikkuli/yamnaya\tv0.0.0-20261019-abc\th1:x=\n", args[2])
				}
				return nil
			}
			cmd := updateCmd(run)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(nil)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			want := [][]string{
				{"install", "github.com/project-kikkuli/yamnaya/cmd/ym@latest"},
				{"env", "GOBIN", "GOPATH"},
				{"version", "-m", tc.wantPath},
			}
			if !slices.EqualFunc(calls, want, slices.Equal) {
				t.Fatalf("go calls = %q, want %q", calls, want)
			}
			for _, s := range []string{"go: downloading", "installed ym v0.0.0-20261019-abc at " + tc.wantPath} {
				if !strings.Contains(out.String(), s) {
					t.Fatalf("output %q lacks %q", out.String(), s)
				}
			}
		})
	}
}

func TestUpdateStopsWhenInstallFails(t *testing.T) {
	var calls int
	cmd := updateCmd(func(out io.Writer, args ...string) error {
		calls++
		return fmt.Errorf("exit status 1")
	})
	cmd.SetOut(io.Discard)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "go install") {
		t.Fatalf("err = %v, want the go install failure", err)
	}
	if calls != 1 {
		t.Fatalf("go ran %d times after install failed, want 1", calls)
	}
}
