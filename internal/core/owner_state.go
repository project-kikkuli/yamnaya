package core

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// ownerHome is the home directory the OS records for this user, which no environment variable can change. Built without cgo,
// macOS keeps its users out of /etc/passwd, so it asks Directory Services.
func ownerHome() (string, error) {
	u, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err == nil {
		return u.HomeDir, nil
	}
	name, nameErr := exec.Command("id", "-un").Output()
	if nameErr != nil {
		return "", err
	}
	out, dsErr := exec.Command("dscl", ".", "-read", "/Users/"+strings.TrimSpace(string(name)), "NFSHomeDirectory").Output()
	if dsErr != nil {
		return "", err
	}
	_, home, ok := strings.Cut(strings.TrimSpace(string(out)), ": ")
	if !ok {
		return "", err
	}
	return home, nil
}

// OwnerStateDir reports whether dir is the state dir of the user the OS says is running, unlike DefaultStateDir not fooled by an
// overridden $HOME. Only that ym may act on the machine's real caches and repositories on its own.
func OwnerStateDir(dir string) bool {
	home, err := ownerHome()
	if err != nil {
		return false
	}
	want := filepath.Join(home, ".local", "state", "yamnaya")
	if filepath.Clean(dir) == want {
		return true
	}
	have, err := os.Stat(dir)
	if err != nil {
		return false
	}
	w, err := os.Stat(want)
	return err == nil && os.SameFile(have, w)
}

// DiskWatchEnv opts a drill's ym serve into the disk watcher; only a state dir under a rehearsal root can.
const DiskWatchEnv = "YAMNAYA_DISK_WATCH"

// WatchesDisk reports whether a ym serve on dir watches free space and starts gc by itself.
func WatchesDisk(dir string) bool {
	return OwnerStateDir(dir) || os.Getenv(DiskWatchEnv) != "" && RehearsalRoot(dir) != ""
}
