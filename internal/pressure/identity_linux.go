package pressure

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func ReadIdentity(pid int) Identity {
	raw, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	id := Identity{Cwd: proc.Cwd(pid)}
	for _, env := range bytes.Split(raw, []byte{0}) {
		if value, ok := strings.CutPrefix(string(env), "CODEX_THREAD_ID="); ok {
			id.Thread = value
			break
		}
	}
	return id
}
