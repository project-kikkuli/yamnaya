package codex

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// platforms names, per GOOS/GOARCH, the npm package holding Codex's binary and the target triple it is built for.
var platforms = map[string][2]string{
	"darwin/arm64": {"codex-darwin-arm64", "aarch64-apple-darwin"},
	"darwin/amd64": {"codex-darwin-x64", "x86_64-apple-darwin"},
	"linux/arm64":  {"codex-linux-arm64", "aarch64-unknown-linux-musl"},
	"linux/amd64":  {"codex-linux-x64", "x86_64-unknown-linux-musl"},
}

// Native is the platform binary that the npm package's node wrapper at path runs, or path itself when it is no such wrapper or
// the binary is missing. Starting it directly keeps a long prompt off a node command line, which endpoint security may kill.
func Native(path string) string {
	real, err := filepath.EvalSymlinks(path)
	p, ok := platforms[runtime.GOOS+"/"+runtime.GOARCH]
	if err != nil || !ok || !strings.HasSuffix(real, ".js") {
		return path
	}
	root := filepath.Dir(filepath.Dir(real))
	for _, vendor := range []string{filepath.Join(root, "node_modules", "@openai", p[0], "vendor"), filepath.Join(filepath.Dir(root), p[0], "vendor"),
		filepath.Join(root, "vendor")} {
		bin := filepath.Join(vendor, p[1], "bin", "codex")
		if info, err := os.Stat(bin); err == nil && !info.IsDir() {
			return bin
		}
	}
	return path
}
