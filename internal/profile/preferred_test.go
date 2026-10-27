package profile

import (
	"path/filepath"
	"testing"
)

func TestPreferAndRemoveKeepTheOtherLogins(t *testing.T) {
	for _, c := range []struct {
		name      string
		prefer    string
		remove    string
		wantIDs   []string
		wantPref  string
		wantError bool
	}{
		{"prefer a login", "two", "", []string{"default", "one", "two"}, "two", false},
		{"default restores the native login", "default", "", []string{"default", "one", "two"}, "", false},
		{"removing the preferred login clears it", "one", "one", []string{"default", "two"}, "", false},
		{"removing another keeps the preference", "two", "one", []string{"default", "two"}, "two", false},
		{"the native login stays", "", "default", []string{"default", "one", "two"}, "", true},
		{"an unknown login is refused", "", "nine", []string{"default", "one", "two"}, "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := isolated(t)
			for _, id := range []string{"one", "two"} {
				if err := Add(dir, Profile{Harness: "claude", ID: id, Root: filepath.Join(t.TempDir(), id)}); err != nil {
					t.Fatal(err)
				}
			}
			if c.prefer != "" {
				if err := Prefer(dir, "claude", c.prefer); err != nil {
					t.Fatal(err)
				}
			}
			if c.remove != "" {
				if err := Remove(dir, "claude", c.remove); (err != nil) != c.wantError {
					t.Fatalf("remove %s: %v", c.remove, err)
				}
			}
			ps, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, p := range ps {
				if p.Harness == "claude" {
					ids = append(ids, p.ID)
				}
			}
			if len(ids) != len(c.wantIDs) {
				t.Fatalf("claude logins %v, want %v", ids, c.wantIDs)
			}
			for i := range ids {
				if ids[i] != c.wantIDs[i] {
					t.Fatalf("claude logins %v, want %v", ids, c.wantIDs)
				}
			}
			if got := Preferred(dir)["claude"]; got != c.wantPref {
				t.Fatalf("preferred %q, want %q", got, c.wantPref)
			}
		})
	}
}
