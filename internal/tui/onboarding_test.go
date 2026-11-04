package tui

import (
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestSessionCountsNameOnboarding(t *testing.T) {
	got := sessionCounts([]core.Live{{Session: core.Session{Status: core.Onboarding}}, {Session: core.Session{Status: core.Busy}}})
	if !strings.Contains(got, "1 onboarding") {
		t.Fatalf("sessionCounts = %q", got)
	}
}
