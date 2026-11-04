package core

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

// lookupOwner reads this user's entry from the OS. Without the osusergo build tag that is the system resolver, on macOS with or
// without cgo; no environment variable changes it, and it fails rather than falling back to $HOME.
var lookupOwner = func() (*user.User, error) { return user.LookupId(strconv.Itoa(os.Getuid())) }

func ownerStateDir() (string, error) {
	u, err := lookupOwner()
	if err != nil {
		return "", err
	}
	return filepath.Join(u.HomeDir, ".local", "state", "yamnaya"), nil
}

// OwnerStateDir reports whether dir is the state dir of the user the OS says is running, unlike DefaultStateDir not fooled by an
// overridden $HOME. Only that ym may act on the machine's real caches and repositories on its own.
func OwnerStateDir(dir string) bool {
	want, err := ownerStateDir()
	if err != nil {
		return false
	}
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

// DiskWatchOff says why a ym serve on dir does not watch free space, or "" when it does. broken marks a host whose real ym cannot
// tell it is the real one, where nobody chose this.
func DiskWatchOff(dir string) (why string, broken bool) {
	if WatchesDisk(dir) {
		return "", false
	}
	want, err := ownerStateDir()
	if err != nil {
		return fmt.Sprintf("cannot read this user's home from the OS: %v", err), true
	}
	return fmt.Sprintf("%s is not this user's state dir %s", dir, want), false
}
