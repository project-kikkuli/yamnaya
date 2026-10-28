package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/project-kikkuli/yamnaya/internal/slot"
)

// keepFix is how the owner clears a warning that launchd does not keep ym serve, without sudo.
const keepFix = "run ym service keep as yourself, or press Y on the board and enter on the keep row; if that fails again, run launchctl bootout gui/$(id -u)/io.yamnaya.ym-serve first. None of it needs sudo"

// RefuseForeign stops ym before it touches any state when it runs as root for another user, as sudo ym does, or as a user
// whose state directory another user owns.
func RefuseForeign() error {
	dirs, err := ownedDirs()
	if err != nil {
		return nil
	}
	owner := -1
	if info, err := os.Stat(dirs[0]); err == nil {
		owner = fileOwner(info)
	}
	return refuseForeign(os.Geteuid(), owner, os.Getenv("SUDO_USER"), accountName, groupName, dirs)
}

func refuseForeign(euid, owner int, sudoUser string, account func(int) string, group func(string) string, dirs []string) error {
	switch {
	case euid == 0 && (sudoUser != "" || owner > 0):
		name := sudoUser
		if name == "" {
			name = account(owner)
		}
		return fmt.Errorf("don't run ym with sudo: it writes root-owned files into %s's ym state. If ym warned that launchd does not keep ym serve, %s. To repair files a root run left behind: %s",
			name, keepFix, chownFix(name, group(name), dirs))
	case owner >= 0 && owner != euid:
		name := account(euid)
		return fmt.Errorf("%s belongs to %s, not %s, so ym cannot keep its state there. To give it back: %s", dirs[0], account(owner), name, chownFix(name, group(name), dirs))
	}
	return nil
}

// OwnershipHint is the command that gives the owner back the files in ym's state another user owns, when err is ym failing on them.
func OwnershipHint(err error) string {
	if err == nil || !errors.Is(err, fs.ErrPermission) && !strings.Contains(err.Error(), "permission denied") {
		return ""
	}
	return foreignWarning()
}

// foreignWarning names the files in ym's state another user owns, as a sudo ym leaves, and the command that gives them back.
func foreignWarning() string {
	dirs, err := ownedDirs()
	if err != nil {
		return ""
	}
	uid := os.Geteuid()
	n, first := foreignFiles(dirs, uid)
	if n == 0 {
		return ""
	}
	name := accountName(uid)
	return fmt.Sprintf("%d files in ym's state belong to another user (%s, as a sudo ym leaves), so ym cannot update them. Fix: %s", n, first, chownFix(name, groupName(name), dirs))
}

// foreignFiles counts the entries under dirs that uid does not own, and names the first.
func foreignFiles(dirs []string, uid int) (int, string) {
	n, first := 0, ""
	for _, dir := range dirs {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if d == nil {
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil || fileOwner(info) == uid {
				return nil
			}
			if n++; first == "" {
				first = path
			}
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		})
	}
	return n, first
}

func ownedDirs() ([]string, error) {
	state, err := stateDir()
	if err != nil {
		return nil, err
	}
	slots, err := slot.Dir()
	if err != nil {
		return nil, err
	}
	return []string{state, slots}, nil
}

func chownFix(name, group string, dirs []string) string {
	return fmt.Sprintf("sudo chown -R %s:%s %s", name, group, strings.Join(dirs, " "))
}

func fileOwner(info fs.FileInfo) int {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

func accountName(uid int) string {
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		return u.Username
	}
	return strconv.Itoa(uid)
}

func groupName(name string) string {
	if u, err := user.Lookup(name); err == nil {
		if g, err := user.LookupGroupId(u.Gid); err == nil {
			return g.Name
		}
		return u.Gid
	}
	return "staff"
}
