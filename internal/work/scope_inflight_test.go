package work_test

import (
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestArchivedScopeInFlightFollowsOpenReviews(t *testing.T) {
	cases := []struct {
		name   string
		review work.Review
		want   bool
	}{
		{"provisional incoming", work.Review{State: "incoming", Provisional: true}, false},
		{"provisional ready", work.Review{State: work.Ready, Provisional: true}, false},
		{"confirmed incoming", work.Review{State: "incoming"}, true},
		{"confirmed ready", work.Review{State: work.Ready}, true},
		{"closed incoming", work.Review{State: "incoming", Closed: true}, false},
		{"dismissed incoming", work.Review{State: "incoming", Dismissed: true}, false},
	}
	for _, tc := range cases {
		for _, state := range []string{"archived", "completed"} {
			t.Run(tc.name+"/"+state, func(t *testing.T) {
				s := work.Scope{State: state, Reviews: map[string]work.Review{"r": tc.review}}
				if got := s.InFlight(); got != tc.want {
					t.Fatalf("InFlight() = %v, want %v", got, tc.want)
				}
			})
		}
	}
}
