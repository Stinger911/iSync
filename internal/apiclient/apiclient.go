// Package apiclient defines the interface iSync uses to talk to iCloud's
// private web API, isolating the sync engine from the reverse-engineered,
// unofficial integration (see INTENT.md Constraints and spec.md NFR-7).
//
// Two implementations satisfy this interface: internal/apiclient/real
// (talks to Apple's actual private web API) and internal/apiclient/fake
// (an in-memory fake used by all automated tests — see spec.md §2.5 and
// plan.md Phase 2/3).
package apiclient

import (
	"context"
	"io"
	"time"
)

// Item describes a single file or folder as reported by iCloud Drive.
type Item struct {
	Path         string
	IsDir        bool
	Size         int64
	ModifiedTime time.Time
	ETag         string // Apple's reported checksum/etag, when available.
}

// Session holds whatever the real client needs to make authenticated
// requests after a successful login (session cookie, trust token, etc.).
// Its concrete shape is defined by internal/credstore; apiclient only
// passes it through opaquely.
type Session struct {
	Raw []byte
}

// TwoFactorChallenge is returned by Authenticate when Apple requires an
// interactive 2FA code before a session can be established.
type TwoFactorChallenge struct {
	// Submit completes the challenge with the code the user received on
	// a trusted device, returning the now-authenticated Session.
	Submit func(ctx context.Context, code string) (Session, error)
}

// Client is the narrow interface the sync engine and CLI depend on. It
// deliberately does not leak HTTP/transport details so it can be backed
// by either the real Apple API or the fake test server.
type Client interface {
	// Authenticate begins a login with an Apple ID and password. If the
	// account requires 2FA, challenge is non-nil and the caller must
	// call challenge.Submit with the code before the session is usable.
	Authenticate(ctx context.Context, appleID, password string) (session Session, challenge *TwoFactorChallenge, err error)

	// List returns the immediate children of path in iCloud Drive.
	List(ctx context.Context, session Session, path string) ([]Item, error)

	// Fetch streams the content of the file at path.
	Fetch(ctx context.Context, session Session, path string) (io.ReadCloser, error)

	// Upload sends content as the file at path, creating or replacing it.
	Upload(ctx context.Context, session Session, path string, content io.Reader, size int64) error

	// Delete removes the file or folder at path.
	Delete(ctx context.Context, session Session, path string) error
}
