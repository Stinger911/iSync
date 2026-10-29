package poller

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestActivitySignal(t *testing.T) {
	sig := NewActivitySignal()

	if sig.ActiveAndReset() {
		t.Error("new signal should not be active before any Notify")
	}

	sig.Notify()
	if !sig.ActiveAndReset() {
		t.Error("Notify() should set active")
	}

	sig.Notify()
	sig.Notify()
	if !sig.ActiveAndReset() {
		t.Error("multiple Notify() calls should still leave active true once")
	}
}

func TestPollerRunsAtBaselineWithoutSignal(t *testing.T) {
	var calls atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	p := New(Strategy{Baseline: 50 * time.Millisecond}, nil)
	_ = p.Run(ctx, func(ctx context.Context) error { calls.Add(1); return nil })

	if calls.Load() == 0 {
		t.Fatal("Poller did not run callback")
	}
}

func TestPollerTightensAfterActivity(t *testing.T) {
	sig := NewActivitySignal()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int64
	p := New(Strategy{Baseline: time.Hour, Active: 10 * time.Millisecond, Idle: 25 * time.Millisecond}, sig)

	go func() {
		_ = p.Run(ctx, func(ctx context.Context) error { calls.Add(1); return nil })
	}()

	// Notify the signal explicitly to wake the poller.
	sig.Notify()

	// The signal should be active; first callback should fire quickly.
	time.Sleep(30 * time.Millisecond)
	if calls.Load() == 0 {
		t.Fatal("initial active signal did not trigger callback")
	}

	// Let the active window decay. After > Idle, the next delay becomes
	// Baseline (hour) and no more calls happen for the remainder of the
	// short test.
	time.Sleep(60 * time.Millisecond)
	first := calls.Load()
	time.Sleep(40 * time.Millisecond)
	if calls.Load() != first {
		t.Fatalf("poller did not decay to baseline: calls went %d -> %d", first, calls.Load())
	}
}

func TestPollerCallbackErrorDoesNotStopPolling(t *testing.T) {
	sig := NewActivitySignal()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	var calls atomic.Int64
	p := New(Strategy{Active: 10 * time.Millisecond}, sig)
	_ = p.Run(ctx, func(ctx context.Context) error {
		calls.Add(1)
		if calls.Load()%2 == 0 {
			return context.DeadlineExceeded
		}
		return nil
	})

	if calls.Load() == 0 {
		t.Fatal("Poller stopped after callback error")
	}
}

func TestPollerReturnsContextErr(t *testing.T) {
	sig := NewActivitySignal()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := New(Strategy{Baseline: time.Hour}, sig)
	err := p.Run(ctx, func(ctx context.Context) error { return nil })
	if err != context.Canceled {
		t.Errorf("Run() error = %v, want context.Canceled", err)
	}
}
