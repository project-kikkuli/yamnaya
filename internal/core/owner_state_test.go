package core

import (
	"errors"
	"os/user"
	"strings"
	"testing"
)

func TestTheWatcherSaysWhyItIsOff(t *testing.T) {
	old := lookupOwner
	defer func() { lookupOwner = old }()
	failed := errors.New("no such user")
	for _, c := range []struct {
		name   string
		err    error
		dir    string
		why    string
		broken bool
	}{
		{"the owner's own state dir", nil, "/home/me/.local/state/yamnaya", "", false},
		{"a scratch state dir", nil, "/scratch/state", "not this user's state dir", false},
		{"the OS cannot say who this is", failed, "/home/me/.local/state/yamnaya", "no such user", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			lookupOwner = func() (*user.User, error) { return &user.User{HomeDir: "/home/me"}, c.err }
			why, broken := DiskWatchOff(c.dir)
			if broken != c.broken || (c.why == "") != (why == "") || !strings.Contains(why, c.why) {
				t.Fatalf("DiskWatchOff = %q, %v", why, broken)
			}
		})
	}
}
