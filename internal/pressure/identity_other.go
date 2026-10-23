//go:build !darwin && !linux

package pressure

import "github.com/project-kikkuli/yamnaya/internal/proc"

func ReadIdentity(pid int) Identity { return Identity{Cwd: proc.Cwd(pid)} }
