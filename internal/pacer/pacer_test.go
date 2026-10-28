package pacer

import (
	"context"
	"testing"
	"time"
)

func TestPacer_StartsAtZeroDelay(t *testing.T) {
	p := New(Config{})
	if got := p.CurrentDelay(); got != 0 {
		t.Errorf("CurrentDelay() = %v, want 0 before any rate-limit signal", got)
	}
}

func TestPacer_OnRateLimited_GrowsAndCaps(t *testing.T) {
	p := New(Config{MinDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond, Multiplier: 2})

	p.OnRateLimited()
	if got := p.CurrentDelay(); got != time.Millisecond {
		t.Errorf("after first OnRateLimited, delay = %v, want %v (bootstrap to MinDelay)", got, time.Millisecond)
	}

	p.OnRateLimited()
	if got := p.CurrentDelay(); got != 2*time.Millisecond {
		t.Errorf("after second OnRateLimited, delay = %v, want %v", got, 2*time.Millisecond)
	}

	for i := 0; i < 10; i++ {
		p.OnRateLimited()
	}
	if got := p.CurrentDelay(); got != 10*time.Millisecond {
		t.Errorf("delay after many OnRateLimited = %v, want capped at %v", got, 10*time.Millisecond)
	}
}

func TestPacer_OnSuccess_DecaysAfterStreak(t *testing.T) {
	p := New(Config{MinDelay: time.Millisecond, MaxDelay: time.Second, Multiplier: 2, Decay: 0.5, DecayAfter: 3})

	p.OnRateLimited() // delay = 1ms
	p.OnRateLimited() // delay = 2ms
	p.OnRateLimited() // delay = 4ms

	// Fewer than DecayAfter successes: no decay yet.
	p.OnSuccess()
	p.OnSuccess()
	if got := p.CurrentDelay(); got != 4*time.Millisecond {
		t.Errorf("delay after 2 successes (< DecayAfter) = %v, want unchanged %v", got, 4*time.Millisecond)
	}

	// Third consecutive success triggers one decay step.
	p.OnSuccess()
	if got := p.CurrentDelay(); got != 2*time.Millisecond {
		t.Errorf("delay after 3rd consecutive success = %v, want %v", got, 2*time.Millisecond)
	}
}

func TestPacer_OnSuccess_FloorsAtMinDelay(t *testing.T) {
	p := New(Config{MinDelay: time.Millisecond, MaxDelay: time.Second, Multiplier: 2, Decay: 0.5, DecayAfter: 1})
	p.OnRateLimited() // delay = 1ms (== MinDelay already)

	for i := 0; i < 5; i++ {
		p.OnSuccess()
	}
	if got := p.CurrentDelay(); got != time.Millisecond {
		t.Errorf("delay after repeated decay = %v, want floored at MinDelay %v", got, time.Millisecond)
	}
}

func TestPacer_Wait_RespectsContextCancellation(t *testing.T) {
	p := New(Config{MinDelay: time.Hour, MaxDelay: time.Hour})
	p.OnRateLimited() // delay = 1h, far longer than the test should wait

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := p.Wait(ctx)
	if err == nil {
		t.Error("Wait() = nil, want context deadline error")
	}
}

func TestLimiter_BoundsConcurrency(t *testing.T) {
	l := NewLimiter(2)
	ctx := context.Background()

	if err := l.Acquire(ctx); err != nil {
		t.Fatalf("Acquire 1 error = %v", err)
	}
	if err := l.Acquire(ctx); err != nil {
		t.Fatalf("Acquire 2 error = %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		_ = l.Acquire(context.Background())
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("third Acquire succeeded before any Release, limiter not bounding concurrency")
	case <-time.After(30 * time.Millisecond):
		// Expected: third acquire is still blocked.
	}

	l.Release()
	select {
	case <-acquired:
		// Expected: releasing one slot lets the third Acquire through.
	case <-time.After(time.Second):
		t.Fatal("third Acquire never unblocked after Release")
	}
	l.Release()
	l.Release()
}
