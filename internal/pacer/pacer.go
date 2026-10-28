// Package pacer implements the adaptive request pacer and bounded
// concurrency limiter shared by internal/apiclient/real, per INTENT.md's
// resolved rate-limiting strategy: a baseline inter-request delay that
// multiplies on rate-limit signals and decays after sustained success,
// combined with a max-concurrent-transfers cap. See plan.md Phase 3.
package pacer

import (
	"context"
	"sync"
	"time"
)

// Pacer tracks an adaptive inter-request delay: it grows (multiplicatively,
// capped) on a rate-limit signal and decays (multiplicatively, floored)
// after a configurable number of consecutive successes. The zero value is
// not usable; construct with New.
type Pacer struct {
	mu sync.Mutex

	delay      time.Duration
	minDelay   time.Duration
	maxDelay   time.Duration
	multiplier float64
	decay      float64
	decayAfter int

	successStreak int
}

// Config tunes a Pacer's behavior. Zero values fall back to sane defaults
// (see New).
type Config struct {
	// MinDelay is the floor the delay never decays below (and the value
	// used to bootstrap the first rate-limit signal). Default 0 (no
	// pacing at all until the first rate-limit signal is seen).
	MinDelay time.Duration
	// MaxDelay is the ceiling the delay never grows past. Default 10s.
	MaxDelay time.Duration
	// Multiplier scales the delay up on each rate-limit signal. Default 2.
	Multiplier float64
	// Decay scales the delay down after DecayAfter consecutive
	// successes. Default 0.5 (halve).
	Decay float64
	// DecayAfter is how many consecutive successes trigger one decay
	// step. Default 5.
	DecayAfter int
}

// New returns a Pacer configured per cfg, applying defaults for any zero
// fields.
func New(cfg Config) *Pacer {
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 10 * time.Second
	}
	if cfg.Multiplier <= 1 {
		cfg.Multiplier = 2
	}
	if cfg.Decay <= 0 || cfg.Decay >= 1 {
		cfg.Decay = 0.5
	}
	if cfg.DecayAfter <= 0 {
		cfg.DecayAfter = 5
	}
	return &Pacer{
		minDelay:   cfg.MinDelay,
		maxDelay:   cfg.MaxDelay,
		multiplier: cfg.Multiplier,
		decay:      cfg.Decay,
		decayAfter: cfg.DecayAfter,
	}
}

// Wait blocks for the current delay, or returns early with ctx.Err() if
// ctx is canceled first. Call it immediately before issuing a request.
func (p *Pacer) Wait(ctx context.Context) error {
	p.mu.Lock()
	d := p.delay
	p.mu.Unlock()

	if d <= 0 {
		return nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// OnRateLimited grows the delay (bootstrapping from MinDelay if this is
// the first signal) and resets the success streak.
func (p *Pacer) OnRateLimited() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.successStreak = 0

	next := p.delay
	if next <= 0 {
		next = p.minDelay
		if next <= 0 {
			next = time.Millisecond
		}
	} else {
		next = time.Duration(float64(next) * p.multiplier)
	}
	if next > p.maxDelay {
		next = p.maxDelay
	}
	p.delay = next
}

// OnSuccess records a successful call. Every DecayAfter consecutive
// successes, the delay decays toward MinDelay.
func (p *Pacer) OnSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.successStreak++
	if p.successStreak < p.decayAfter {
		return
	}
	p.successStreak = 0

	next := time.Duration(float64(p.delay) * p.decay)
	if next < p.minDelay {
		next = p.minDelay
	}
	p.delay = next
}

// CurrentDelay returns the pacer's current inter-request delay, mainly
// useful for `isync status` diagnostics (spec.md §2.4).
func (p *Pacer) CurrentDelay() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.delay
}

// Limiter bounds the number of concurrent operations (transfers), per
// INTENT.md's resolved rate-limiting decision: a separate, independent
// control from the pacer's inter-request delay.
type Limiter struct {
	sem chan struct{}
}

// NewLimiter returns a Limiter allowing up to max concurrent holders.
func NewLimiter(max int) *Limiter {
	if max <= 0 {
		max = 1
	}
	return &Limiter{sem: make(chan struct{}, max)}
}

// Acquire blocks until a slot is free, or ctx is canceled.
func (l *Limiter) Acquire(ctx context.Context) error {
	select {
	case l.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release frees a previously-Acquired slot.
func (l *Limiter) Release() {
	<-l.sem
}
