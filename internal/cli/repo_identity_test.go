package cli

import "testing"

func TestAHostsRepositoryKeepsItsOriginIdentityAndQualifiesOnlyAPath(t *testing.T) {
	for _, c := range []struct {
		name, id, key, want string
	}{
		{"origin from a current host", "github.com/o/app", "/home/r/app/.git", "github.com/o/app"},
		{"no remote on a current host", "/home/r/scratch/.git", "/home/r/scratch/.git", "box//home/r/scratch/.git"},
		{"a host on an older ym", "", "/home/r/app/.git", "box//home/r/app/.git"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := remoteBoard("box", boardJSON{
				Sessions: []sessionJSON{{Harness: "claude", ID: "s", RepoKey: c.key, RepoID: c.id}},
				Dormant:  []dormantJSON{{Key: "k", Session: "d", RepoKey: c.key, RepoID: c.id}},
			})
			if got := r.Sessions[0].RepoID; got != c.want {
				t.Errorf("session repo id %q, want %q", got, c.want)
			}
			if got := r.Dormant[0].RepoID; got != c.want {
				t.Errorf("dormant repo id %q, want %q", got, c.want)
			}
			if got := r.Sessions[0].RepoKey; got != "box/"+c.key {
				t.Errorf("session repo key %q, want it qualified by host", got)
			}
		})
	}
}
