package apiclient

import "errors"

// Well-known, typed errors apiclient.Client implementations (real and
// fake) return so callers — especially internal/pacer and internal/
// syncengine's retry logic — can react uniformly regardless of which
// implementation is in use. See plan.md Phase 2/3 and INTENT.md's
// resolved rate-limiting and session-handling decisions.
var (
	// ErrInvalidCredentials is returned by Authenticate when the Apple ID
	// or password (or, during a 2FA challenge, the submitted code) is
	// wrong. Not retryable without new input from the user.
	ErrInvalidCredentials = errors.New("apiclient: invalid credentials")

	// ErrSessionExpired is returned by any operation made with a Session
	// that is no longer valid (expired trust token, revoked session,
	// etc.). Callers must re-authenticate; this is never retried
	// automatically by the pacer.
	ErrSessionExpired = errors.New("apiclient: session expired, re-authentication required")

	// ErrRateLimited is returned when the API signals the caller is
	// being throttled (HTTP 429, or the 400/503 patterns Apple's private
	// API is known to return under load). internal/pacer treats this as
	// a signal to increase its inter-request delay.
	ErrRateLimited = errors.New("apiclient: rate limited")

	// ErrTransient represents a retryable failure that isn't a rate-limit
	// signal specifically (e.g. a transient network/server error).
	// Bounded exponential-backoff retry applies; it is not treated as a
	// rate-limit signal by the pacer.
	ErrTransient = errors.New("apiclient: transient error, retry")

	// ErrNotFound is returned by List/Fetch/Delete when the given path
	// does not exist.
	ErrNotFound = errors.New("apiclient: path not found")

	// ErrFileTooLarge is returned by Upload when content exceeds whatever
	// size limit the backend enforces (see INTENT.md's resolved
	// max-file-size decision — this is enforced by the sync engine
	// before upload is attempted, but a backend may also reject outsized
	// content directly).
	ErrFileTooLarge = errors.New("apiclient: file too large")
)
