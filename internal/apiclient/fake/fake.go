// Package fake implements apiclient.Client as an in-memory fake server,
// with injectable behaviors (rate-limit responses, transient errors,
// session expiry, 2FA challenges) so all automated tests can run without
// touching real Apple infrastructure. See spec.md §2.5 and plan.md
// Phase 2.
package fake

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Stinger911/iSync/internal/apiclient"
)

// item is one file or directory in an account's in-memory iCloud Drive.
type item struct {
	isDir   bool
	content []byte
	mtime   time.Time
	etag    string
}

// account is one registered fake Apple ID.
type account struct {
	password          string
	twoFactorRequired bool
	twoFactorCode     string
	fs                map[string]*item // path -> item, keys are clean absolute paths like "/Documents"
}

// Server is an in-memory fake implementation of apiclient.Client. The zero
// value is not usable; construct with NewServer.
type Server struct {
	mu       sync.Mutex
	accounts map[string]*account
	sessions map[string]string // session token -> apple ID

	// Injected-failure state, consulted (and mutated) by every method via
	// nextInjectedError. See SetRateLimitEvery / FailNextRequests.
	requestCount   int
	rateLimitEvery int
	failNextN      int
}

// NewServer returns an empty fake server with no registered accounts.
func NewServer() *Server {
	return &Server{
		accounts: make(map[string]*account),
		sessions: make(map[string]string),
	}
}

// AddAccount registers a fake Apple ID + password, with no 2FA
// requirement by default. Use RequireTwoFactor to add one.
func (s *Server) AddAccount(appleID, password string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accounts[appleID] = &account{
		password: password,
		fs:       make(map[string]*item),
	}
}

// RequireTwoFactor configures appleID's account to require an interactive
// 2FA code on every Authenticate call, which must match code.
func (s *Server) RequireTwoFactor(appleID, code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if acct, ok := s.accounts[appleID]; ok {
		acct.twoFactorRequired = true
		acct.twoFactorCode = code
	}
}

// SetRateLimitEvery configures the server to return apiclient.ErrRateLimited
// on every Nth call to any Client method, across all accounts/sessions. 0
// disables rate-limit injection (the default).
func (s *Server) SetRateLimitEvery(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rateLimitEvery = n
	s.requestCount = 0
}

// FailNextRequests makes the next n calls to any Client method return
// apiclient.ErrTransient, decrementing once per call. Use this to test
// bounded-retry behavior in internal/pacer and internal/syncengine.
func (s *Server) FailNextRequests(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNextN = n
}

// ExpireSession invalidates session so that any subsequent call using it
// returns apiclient.ErrSessionExpired, simulating a trust-token/session
// expiry the real API might impose.
func (s *Server) ExpireSession(session apiclient.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, string(session.Raw))
}

// nextInjectedError consults and advances the injected-failure counters.
// Call it first in every Client method, before touching account state.
func (s *Server) nextInjectedError() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.requestCount++

	if s.failNextN > 0 {
		s.failNextN--
		return apiclient.ErrTransient
	}
	if s.rateLimitEvery > 0 && s.requestCount%s.rateLimitEvery == 0 {
		return apiclient.ErrRateLimited
	}
	return nil
}

// resolveSession looks up the account for a session token, or
// apiclient.ErrSessionExpired if it's missing/invalidated.
func (s *Server) resolveSession(session apiclient.Session) (*account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	appleID, ok := s.sessions[string(session.Raw)]
	if !ok {
		return nil, apiclient.ErrSessionExpired
	}
	acct, ok := s.accounts[appleID]
	if !ok {
		return nil, apiclient.ErrSessionExpired
	}
	return acct, nil
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func cleanPath(p string) string {
	cp := path.Clean("/" + p)
	return cp
}

func etagFor(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])[:16]
}

// Authenticate implements apiclient.Client.
func (s *Server) Authenticate(ctx context.Context, appleID, password string) (apiclient.Session, *apiclient.TwoFactorChallenge, error) {
	if err := s.nextInjectedError(); err != nil {
		return apiclient.Session{}, nil, err
	}

	s.mu.Lock()
	acct, ok := s.accounts[appleID]
	s.mu.Unlock()
	if !ok || acct.password != password {
		return apiclient.Session{}, nil, apiclient.ErrInvalidCredentials
	}

	if !acct.twoFactorRequired {
		return s.establishSession(appleID), nil, nil
	}

	challenge := &apiclient.TwoFactorChallenge{
		Submit: func(ctx context.Context, code string) (apiclient.Session, error) {
			if code != acct.twoFactorCode {
				return apiclient.Session{}, apiclient.ErrInvalidCredentials
			}
			return s.establishSession(appleID), nil
		},
	}
	return apiclient.Session{}, challenge, nil
}

func (s *Server) establishSession(appleID string) apiclient.Session {
	token := newToken()
	s.mu.Lock()
	s.sessions[token] = appleID
	s.mu.Unlock()
	return apiclient.Session{Raw: []byte(token)}
}

// List implements apiclient.Client.
func (s *Server) List(ctx context.Context, session apiclient.Session, p string) ([]apiclient.Item, error) {
	if err := s.nextInjectedError(); err != nil {
		return nil, err
	}
	acct, err := s.resolveSession(session)
	if err != nil {
		return nil, err
	}

	target := cleanPath(p)

	s.mu.Lock()
	defer s.mu.Unlock()

	if target != "/" {
		if it, ok := acct.fs[target]; !ok || !it.isDir {
			return nil, apiclient.ErrNotFound
		}
	}

	var out []apiclient.Item
	for itemPath, it := range acct.fs {
		if path.Dir(itemPath) == target {
			out = append(out, apiclient.Item{
				Path:         itemPath,
				IsDir:        it.isDir,
				Size:         int64(len(it.content)),
				ModifiedTime: it.mtime,
				ETag:         it.etag,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Fetch implements apiclient.Client.
func (s *Server) Fetch(ctx context.Context, session apiclient.Session, p string) (io.ReadCloser, error) {
	if err := s.nextInjectedError(); err != nil {
		return nil, err
	}
	acct, err := s.resolveSession(session)
	if err != nil {
		return nil, err
	}

	target := cleanPath(p)

	s.mu.Lock()
	it, ok := acct.fs[target]
	s.mu.Unlock()

	if !ok {
		return nil, apiclient.ErrNotFound
	}
	if it.isDir {
		return nil, fmt.Errorf("apiclient/fake: %s is a directory", target)
	}

	return io.NopCloser(bytes.NewReader(it.content)), nil
}

// Upload implements apiclient.Client. Missing parent directories are
// created implicitly, matching how a real filesystem-backed sync target
// behaves; iSync's own folder-pair setup is responsible for ensuring the
// configured iCloud Drive root actually exists before registering a pair.
func (s *Server) Upload(ctx context.Context, session apiclient.Session, p string, content io.Reader, size int64) error {
	if err := s.nextInjectedError(); err != nil {
		return err
	}
	acct, err := s.resolveSession(session)
	if err != nil {
		return err
	}

	data, err := io.ReadAll(content)
	if err != nil {
		return fmt.Errorf("apiclient/fake: reading upload content: %w", err)
	}

	target := cleanPath(p)

	s.mu.Lock()
	defer s.mu.Unlock()

	ensureParentDirs(acct.fs, target)

	acct.fs[target] = &item{
		isDir:   false,
		content: data,
		mtime:   time.Now(),
		etag:    etagFor(data),
	}
	return nil
}

// ensureParentDirs creates implicit directory entries for every ancestor
// of target that doesn't already exist. Caller must hold s.mu.
func ensureParentDirs(fs map[string]*item, target string) {
	dir := path.Dir(target)
	for dir != "/" && dir != "." {
		if _, ok := fs[dir]; !ok {
			fs[dir] = &item{isDir: true, mtime: time.Now()}
		}
		dir = path.Dir(dir)
	}
}

// Delete implements apiclient.Client. Deleting a directory removes it and
// everything beneath it.
func (s *Server) Delete(ctx context.Context, session apiclient.Session, p string) error {
	if err := s.nextInjectedError(); err != nil {
		return err
	}
	acct, err := s.resolveSession(session)
	if err != nil {
		return err
	}

	target := cleanPath(p)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := acct.fs[target]; !ok {
		return apiclient.ErrNotFound
	}

	prefix := target + "/"
	for itemPath := range acct.fs {
		if itemPath == target || strings.HasPrefix(itemPath, prefix) {
			delete(acct.fs, itemPath)
		}
	}
	return nil
}

var _ apiclient.Client = (*Server)(nil)
