package slot

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLanesSerializeTheirCommandsWithoutTakingMachineSlots(t *testing.T) {
	p := pool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	claim := Claim{PID: os.Getpid(), Command: []string{"go", "test"}, Since: time.Now()}
	leave, err := p.Lane(ctx, "yamnaya-test", claim, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer leave()
	waiting := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		next, err := p.Lane(ctx, "yamnaya-test", claim, func() { close(waiting) })
		if next != nil {
			next()
		}
		done <- err
	}()
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	other, err := p.Lane(ctx, "another-repo", Claim{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	other()
	st, err := p.Status()
	if err != nil || len(st.Holders) != 0 || len(st.Waiters) != 1 || st.Waiters[0].Lane != "yamnaya-test" {
		t.Fatalf("lane waiting status: %+v, %v", st, err)
	}
	select {
	case err := <-done:
		t.Fatalf("same lane entered before release: %v", err)
	default:
	}
	leave()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	st, err = p.Status()
	if err != nil || len(st.Waiters) != 0 {
		t.Fatalf("released lane status: %+v, %v", st, err)
	}
}

func TestCanceledLaneWaiterLeavesNoClaimAndReleasesItsDescriptor(t *testing.T) {
	p := pool(t)
	leave, err := p.Lane(context.Background(), "test/../../lane", Claim{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer leave()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = p.Lane(ctx, "test/../../lane", Claim{PID: os.Getpid()}, cancel)
	if err != context.Canceled {
		t.Fatalf("lane wait: %v", err)
	}
	if st, _ := p.Status(); len(st.Waiters) != 0 {
		t.Fatalf("canceled waiter left %+v", st.Waiters)
	}
	leave()
	next, err := p.Lane(context.Background(), "test/../../lane", Claim{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	next()
}
