package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestScheduler_serializes_overlapping_ticks(t *testing.T) {
	var active atomic.Int32
	var maxActive atomic.Int32
	var calls atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})

	tick := func(ctx context.Context) {
		call := calls.Add(1)
		current := active.Add(1)
		for {
			max := maxActive.Load()
			if current <= max || maxActive.CompareAndSwap(max, current) {
				break
			}
		}
		defer active.Add(-1)

		if call == 1 {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}

	s := New(time.Hour, tick)
	s.Start()
	defer s.Stop()

	firstDone := make(chan struct{})
	go func() {
		s.RunNow()
		close(firstDone)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first refresh did not start")
	}

	secondDone := make(chan struct{})
	go func() {
		s.RunNow()
		close(secondDone)
	}()
	select {
	case <-secondDone:
		t.Fatal("second refresh ran before first refresh completed")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first refresh did not finish")
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second refresh did not finish")
	}

	if got := maxActive.Load(); got != 1 {
		t.Fatalf("maximum concurrent ticks = %d, want 1", got)
	}
}
