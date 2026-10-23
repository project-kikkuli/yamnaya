package codex

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNativeIsTheBinaryTheNpmWrapperRuns(t *testing.T) {
	p, ok := platforms[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		t.Skip("no codex build for this platform")
	}
	for _, c := range []struct {
		name, binary string
		js           bool
	}{
		{"nested platform package", filepath.Join("@openai", "codex", "node_modules", "@openai", p[0], "vendor", p[1], "bin", "codex"), true},
		{"hoisted platform package", filepath.Join("@openai", p[0], "vendor", p[1], "bin", "codex"), true},
		{"vendored in the package", filepath.Join("@openai", "codex", "vendor", p[1], "bin", "codex"), true},
		{"wrapper without its binary", "", true},
		{"a native install", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			write := func(rel string) string {
				path := filepath.Join(root, rel)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				return path
			}
			target := write(filepath.Join("bin", "codex-native"))
			if c.js {
				target = write(filepath.Join("@openai", "codex", "bin", "codex.js"))
			}
			link := filepath.Join(root, "codex")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			want := link
			if c.binary != "" {
				want = write(c.binary)
			}
			if got := Native(link); got != want {
				t.Fatalf("Native = %s, want %s", got, want)
			}
		})
	}
}
