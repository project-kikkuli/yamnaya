package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func preferredPath(dir string) string { return filepath.Join(dir, "profile-preferred.json") }

// Preferred is the profile each harness launches on when none is named, by harness; a harness missing from it uses default.
func Preferred(dir string) map[string]string {
	out := map[string]string{}
	if b, err := os.ReadFile(preferredPath(dir)); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

// Prefer makes id the profile harness launches on when none is named; default restores the native login.
func Prefer(dir, harness, id string) error {
	return locked(dir, func(ps []Profile) ([]Profile, error) {
		if _, err := Find(ps, harness, id); err != nil {
			return nil, err
		}
		pref := Preferred(dir)
		if ID(id) == "default" {
			delete(pref, harness)
		} else {
			pref[harness] = id
		}
		b, err := json.MarshalIndent(pref, "", "  ")
		if err != nil {
			return nil, err
		}
		return nil, Write(preferredPath(dir), b)
	})
}

// Remove unregisters a profile, leaving its root and the credentials in it on disk; launches named for it fail until it is added again.
func Remove(dir, harness, id string) error {
	if ID(id) == "default" {
		return errors.New("the default profile is the native login and cannot be removed")
	}
	return locked(dir, func(ps []Profile) ([]Profile, error) {
		if _, err := Find(ps, harness, id); err != nil {
			return nil, err
		}
		saved := []Profile{}
		for _, q := range ps {
			if q.ID != "default" && (q.Harness != harness || q.ID != id) {
				saved = append(saved, q)
			}
		}
		if pref := Preferred(dir); pref[harness] == id {
			delete(pref, harness)
			b, err := json.MarshalIndent(pref, "", "  ")
			if err != nil {
				return nil, err
			}
			if err = Write(preferredPath(dir), b); err != nil {
				return nil, err
			}
		}
		return saved, nil
	})
}

// locked runs f on the registered profiles under the profiles lock and saves what it returns, unless that is nil.
func locked(dir string, f func([]Profile) ([]Profile, error)) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path(dir)+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	ps, err := Load(dir)
	if err != nil {
		return err
	}
	saved, err := f(ps)
	if err != nil || saved == nil {
		return err
	}
	b, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return fmt.Errorf("profiles: %w", err)
	}
	return Write(path(dir), b)
}
