package poller

import (
	"context"
	"sync"
	"time"
)

// Strategy describes the three polling cadences used by the poller.
type Strategy struct {
	Idle     time.Duration
	Active   time.Duration
	Baseline time.Duration
}

// DefaultStrategy returns the resolved cadences from INTENT.md/spec.md.
func DefaultStrategy() Strategy {
	return Strategy{
		Idle:     5 * time.Minute,
		Active:   15 * time.Second,
		Baseline: 60 * time.Second,
	}
}

// ActivitySignal lets the watcher and the poller mutually tighten each
// other's cadence. It is a non-blocking "poke" channel: sending wakes the
// poller immediately and resets the active-timer; if no one drains it,
// sends are dropped.
type ActivitySignal struct {
	mu     sync.Mutex
	poke   chan struct{}
	active bool
}

// NewActivitySignal returns a shared signal.
func NewActivitySignal() *ActivitySignal {
	return &ActivitySignal{poke: make(chan struct{}, 1)}
}

// Notify records activity. It is safe to call from any goroutine and is
// intentionally non-blocking: one buffered slot catches the typical case,
// and duplicate pokes while the poller is already woken are dropped.
func (a *ActivitySignal) Notify() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active = true
	select {
	case a.poke <- struct{}{}:
	default:
	}
}

// Chan returns the channel the poller blocks on. The poller should not
// close it; only the signal owner should call Close.
func (a *ActivitySignal) Chan() <-chan struct{} { return a.poke }

// ActiveAndReset reports whether activity was recorded since the last
// reset and clears the flag.
func (a *ActivitySignal) ActiveAndReset() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	active := a.active
	a.active = false
	return active
}

// Close releases the signal. After Close, Notify() is a no-op.
func (a *ActivitySignal) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.poke != nil {
		close(a.poke)
		a.poke = nil
	}
}

// Poller runs a callback at adaptive intervals. The interval is Baseline by
// default, tightens to Active after any activity on signal, and decays to
// Idle after no activity for the idle duration. The poller stops when ctx
// is canceled.
type Poller struct {
	strategy Strategy
	signal   *ActivitySignal
	now      func() time.Time
}

// Option configures a Poller.
type Option func(*Poller)

// WithClock overrides the time source (used in tests).
func WithClock(now func() time.Time) Option {
	return func(p *Poller) { p.now = now }
}

// New returns a Poller. If signal is nil, the poller runs at Baseline
// forever.
func New(strategy Strategy, signal *ActivitySignal, opts ...Option) *Poller {
	p := &Poller{
		strategy: strategy,
		signal:   signal,
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Run invokes callback repeatedly until ctx is canceled. It returns ctx.Err().
func (p *Poller) Run(ctx context.Context, callback func(ctx context.Context) error) error {
	lastActive := time.Time{}

	for {
		delay := p.strategy.Baseline
		if p.signal != nil {
			if p.signal.ActiveAndReset() || (!lastActive.IsZero() && p.now().Sub(lastActive) < p.strategy.Idle) {
				delay = p.strategy.Active
			} else if !lastActive.IsZero() {
				delay = p.strategy.Idle
			}
		}

		timer := time.NewTimer(delay)
		var waitCh <-chan struct{}
		if p.signal != nil {
			waitCh = p.signal.Chan()
		}

		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		case <-waitCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if p.signal.ActiveAndReset() {
				lastActive = p.now()
			}
		}

		_ = callback(ctx)
	}
}
