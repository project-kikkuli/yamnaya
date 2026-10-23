package slot

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/pressure"
)

func TestOverloadWaiterIsVisibleWithoutTakingAFreeSlotAndRunsOnRecovery(t *testing.T) {
	p := pool(t)
	if err := (pressure.State{Active: true, Holding: true}).Save(p.Dir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		release, _, err := p.Acquire(ctx, Claim{PID: os.Getpid(), Command: []string{"go", "test"}, Since: time.Now()}, func() { close(waiting) })
		if err == nil {
			release()
		}
		done <- err
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("waiter did not stop")
		}
	}()
	<-waiting
	status, err := p.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Holders) != 0 || len(status.Waiters) != 1 || !status.Pressure.Holding {
		t.Fatalf("status %+v", status)
	}
	if err := (pressure.State{Active: true, Holding: false}).Save(p.Dir); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		done <- nil
	case <-time.After(time.Second):
		t.Fatal("waiter stayed blocked after recovery")
	}
}

func TestCancelDuringOverloadRemovesWaitingClaim(t *testing.T) {
	p := pool(t)
	if err := (pressure.State{Active: true, Holding: true}).Save(p.Dir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, _, err := p.Acquire(ctx, Claim{PID: os.Getpid()}, cancel)
	if err != context.Canceled {
		t.Fatal(err)
	}
	status, err := p.Status()
	if err != nil || len(status.Waiters) != 0 || len(status.Holders) != 0 {
		t.Fatal(status, err)
	}
}
