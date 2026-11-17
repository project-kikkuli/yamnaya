package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnlyAnExplicitServiceStartBringsBackAServe(t *testing.T) {
	for _, c := range []struct {
		name   string
		served bool
		args   []string
		want   bool
	}{
		{"state dir that had a serve", true, []string{"goal", "authority"}, false},
		{"explicit service start", true, []string{"service", "ensure"}, true},
		{"fresh state dir", false, []string{"goal", "authority"}, false},
		{"ym throttle, which must not bring agents back", true, []string{"throttle"}, false},
		{"ym unthrottle", true, []string{"unthrottle"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("YAMNAYA_STATE_DIR", dir)
			if c.served {
				if err := os.WriteFile(filepath.Join(dir, "work-service.lock"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			started := false
			ensureService = func() error { started = true; return nil }
			t.Cleanup(func() { ensureService = ensureWorkService })
			root := NewRoot()
			root.SetArgs(c.args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if started != c.want {
				t.Fatalf("ym %v started serve %v, want %v", c.args, started, c.want)
			}
		})
	}
}

func TestRemoteSnapshotAndRelayReadsDoNotRequireAServiceStart(t *testing.T) {
	for _, args := range [][]string{{"board"}, {"relay", "sync"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("YAMNAYA_STATE_DIR", dir)
			t.Setenv("YAMNAYA_NO_SERVE", "0")
			// Deny service admission before it can spawn any process.
			if err := os.Mkdir(filepath.Join(dir, "work-start.lock"), 0o700); err != nil {
				t.Fatal(err)
			}
			snapshot := []byte(`{"self":"vm","at":"2026-11-17T00:00:00Z"}`)
			if err := os.WriteFile(snapshotPath(dir), snapshot, 0o600); err != nil {
				t.Fatal(err)
			}
			root := NewRoot()
			root.SetArgs(args)
			root.SetIn(strings.NewReader(`{"as":"vm","from":"laptop"}`))
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			if err := root.Execute(); err != nil {
				t.Fatalf("remote read required service admission: %v", err)
			}
			if !json.Valid(out.Bytes()) {
				t.Fatalf("remote read returned invalid JSON: %s", out.Bytes())
			}
			if args[0] == "board" && !bytes.Equal(bytes.TrimSpace(out.Bytes()), snapshot) {
				t.Fatalf("snapshot changed: %s", out.Bytes())
			}
			for _, name := range []string{"work-service.lock", "work-service.json"} {
				if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
					t.Fatalf("remote read created service state %s: %v", name, err)
				}
			}
		})
	}
}
