package slot

import (
	"context"
	"os"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/pressure"
)

func TestOverloadHoldsNewSlotsOnlyWhileOneIsHeld(t *testing.T) {
	for _, c := range []struct {
		name  string
		held  int
		grant bool
	}{
		{"idle pool grants one despite load", 0, true},
		{"busy pool holds the rest", 1, false},
		{"busier pool holds the rest", 3, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := pool(t)
			p.Capacity = 4
			for range c.held {
				release, _, err := p.Acquire(context.Background(), Claim{PID: os.Getpid()}, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			if err := (pressure.State{Active: true, Holding: true, Load: 201, Cores: 12}).Save(p.Dir); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			waited := false
			release, n, err := p.Acquire(ctx, Claim{PID: os.Getpid(), Command: []string{"go", "test"}}, func() {
				waited = true
				status, err := p.Status()
				if err != nil || len(status.Holders) != c.held || len(status.Waiters) != 1 {
					t.Errorf("waiting status %+v, %v", status, err)
				}
				if err := (pressure.State{Active: true}).Save(p.Dir); err != nil {
					t.Error(err)
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			release()
			if waited == c.grant || n != c.held+1 {
				t.Fatalf("held %d: got slot %d, waited %v", c.held, n, waited)
			}
		})
	}
}

func TestCancelDuringOverloadRemovesWaitingClaim(t *testing.T) {
	p := pool(t)
	release, _, err := p.Acquire(context.Background(), Claim{PID: os.Getpid()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := (pressure.State{Active: true, Holding: true}).Save(p.Dir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, _, err = p.Acquire(ctx, Claim{PID: os.Getpid()}, cancel)
	if err != context.Canceled {
		t.Fatal(err)
	}
	status, err := p.Status()
	if err != nil || len(status.Waiters) != 0 || len(status.Holders) != 1 {
		t.Fatal(status, err)
	}
}
