package pacer

import (
	"context"
	"errors"
	"io"

	"github.com/Stinger911/iSync/internal/apiclient"
)

// Policy bundles the pacer, concurrency limiter, and retry bound applied
// uniformly to every apiclient.Client call, regardless of backend (real
// or fake). See plan.md Phase 3.
type Policy struct {
	Pacer       *Pacer
	Limiter     *Limiter
	MaxAttempts int // total attempts per call, including the first; default 5.
}

// Wrap returns an apiclient.Client that applies policy around every call
// to underlying: pacing + bounded concurrency before each attempt, and
// bounded retry for apiclient.ErrRateLimited / apiclient.ErrTransient.
// Non-retryable errors (ErrInvalidCredentials, ErrSessionExpired,
// ErrNotFound, ErrFileTooLarge, or anything else) are returned
// immediately on the first occurrence.
func Wrap(underlying apiclient.Client, policy Policy) apiclient.Client {
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = 5
	}
	return &pacedClient{underlying: underlying, policy: policy}
}

type pacedClient struct {
	underlying apiclient.Client
	policy     Policy
}

// do runs fn under the policy's pacing/concurrency/retry rules.
func do(ctx context.Context, p Policy, fn func(ctx context.Context) error) error {
	var lastErr error

	for attempt := 0; attempt < p.MaxAttempts; attempt++ {
		if p.Limiter != nil {
			if err := p.Limiter.Acquire(ctx); err != nil {
				return err
			}
		}
		if p.Pacer != nil {
			if err := p.Pacer.Wait(ctx); err != nil {
				if p.Limiter != nil {
					p.Limiter.Release()
				}
				return err
			}
		}

		err := fn(ctx)

		if p.Limiter != nil {
			p.Limiter.Release()
		}

		if err == nil {
			if p.Pacer != nil {
				p.Pacer.OnSuccess()
			}
			return nil
		}

		lastErr = err

		switch {
		case errors.Is(err, apiclient.ErrRateLimited):
			if p.Pacer != nil {
				p.Pacer.OnRateLimited()
			}
			continue
		case errors.Is(err, apiclient.ErrTransient):
			// Retryable, but not a rate-limit signal: don't touch the
			// pacer's delay for this, just retry within MaxAttempts.
			continue
		default:
			return err
		}
	}

	return lastErr
}

func (c *pacedClient) Authenticate(ctx context.Context, appleID, password string) (apiclient.Session, *apiclient.TwoFactorChallenge, error) {
	var session apiclient.Session
	var challenge *apiclient.TwoFactorChallenge
	err := do(ctx, c.policy, func(ctx context.Context) error {
		s, ch, err := c.underlying.Authenticate(ctx, appleID, password)
		session, challenge = s, ch
		return err
	})
	return session, challenge, err
}

func (c *pacedClient) List(ctx context.Context, session apiclient.Session, path string) ([]apiclient.Item, error) {
	var items []apiclient.Item
	err := do(ctx, c.policy, func(ctx context.Context) error {
		var err error
		items, err = c.underlying.List(ctx, session, path)
		return err
	})
	return items, err
}

func (c *pacedClient) Fetch(ctx context.Context, session apiclient.Session, path string) (io.ReadCloser, error) {
	var rc io.ReadCloser
	err := do(ctx, c.policy, func(ctx context.Context) error {
		var err error
		rc, err = c.underlying.Fetch(ctx, session, path)
		return err
	})
	return rc, err
}

func (c *pacedClient) Upload(ctx context.Context, session apiclient.Session, path string, content io.Reader, size int64) error {
	// An io.Reader is normally one-shot. Retrying it by reading the full
	// file into memory would make the configured 10 GiB maximum-file size
	// an equally large memory allocation. Retry only when the caller gave
	// us a rewindable stream; otherwise make one attempt and preserve
	// streaming behavior.
	policy := c.policy
	seeker, rewindable := content.(io.Seeker)
	var start int64
	if rewindable {
		var err error
		start, err = seeker.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
	} else {
		policy.MaxAttempts = 1
	}

	return do(ctx, policy, func(ctx context.Context) error {
		if rewindable {
			if _, err := seeker.Seek(start, io.SeekStart); err != nil {
				return err
			}
		}
		return c.underlying.Upload(ctx, session, path, content, size)
	})
}

func (c *pacedClient) Delete(ctx context.Context, session apiclient.Session, path string) error {
	return do(ctx, c.policy, func(ctx context.Context) error {
		return c.underlying.Delete(ctx, session, path)
	})
}

var _ apiclient.Client = (*pacedClient)(nil)
