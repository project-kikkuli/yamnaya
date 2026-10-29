package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnyYmCommandBringsBackAServeThatDied(t *testing.T) {
	for _, c := range []struct {
		name   string
		served bool
		args   []string
		want   bool
	}{
		{"state dir that had a serve", true, []string{"goal", "authority"}, true},
		{"fresh state dir", false, []string{"goal", "authority"}, false},
		{"ym throttle, which must not bring agents back", true, []string{"throttle"}, false},
		{"ym unthrottle", true, []string{"unthrottle"}, true},
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
