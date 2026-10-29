package cli

import (
	"encoding/json"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestSessionAncestrySurvivesHostSnapshot(t *testing.T) {
	for _, parent := range []string{"", "lead", "codex:work@lead"} {
		t.Run(parent, func(t *testing.T) {
			b := core.Board{Sessions: []core.Live{{Session: core.Session{ID: "child", Parent: parent}}}}
			raw, err := json.Marshal(boardJSON{Sessions: sessionsJSON(&core.Core{}, b)})
			if err != nil {
				t.Fatal(err)
			}
			var decoded boardJSON
			if err = json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			for _, host := range []string{"one", "two"} {
				r := remoteBoard(host, decoded)
				local := core.Board{Sessions: r.Sessions}
				want := ""
				if parent != "" {
					want = host + "/" + parent
				}
				if got := local.ParentOf(host + "/child"); got != want {
					t.Fatalf("parent on %s = %q want %q", host, got, want)
				}
			}
		})
	}
}
