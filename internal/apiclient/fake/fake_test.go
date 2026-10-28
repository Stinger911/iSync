package fake

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/Stinger911/iSync/internal/apiclient"
)

func authenticate(t *testing.T, s *Server, appleID, password string) apiclient.Session {
	t.Helper()
	session, challenge, err := s.Authenticate(context.Background(), appleID, password)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if challenge != nil {
		t.Fatalf("Authenticate() returned an unexpected 2FA challenge")
	}
	return session
}

func TestAuthenticate_Success(t *testing.T) {
	s := NewServer()
	s.AddAccount("user@example.com", "hunter2")

	session := authenticate(t, s, "user@example.com", "hunter2")
	if len(session.Raw) == 0 {
		t.Error("Authenticate() returned an empty session")
	}
}

func TestAuthenticate_WrongPassword(t *testing.T) {
	s := NewServer()
	s.AddAccount("user@example.com", "hunter2")

	_, _, err := s.Authenticate(context.Background(), "user@example.com", "wrong")
	if !errors.Is(err, apiclient.ErrInvalidCredentials) {
		t.Errorf("Authenticate() error = %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthenticate_UnknownAccount(t *testing.T) {
	s := NewServer()
	_, _, err := s.Authenticate(context.Background(), "nobody@example.com", "x")
	if !errors.Is(err, apiclient.ErrInvalidCredentials) {
		t.Errorf("Authenticate() error = %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthenticate_TwoFactorFlow(t *testing.T) {
	s := NewServer()
	s.AddAccount("user@example.com", "hunter2")
	s.RequireTwoFactor("user@example.com", "123456")

	session, challenge, err := s.Authenticate(context.Background(), "user@example.com", "hunter2")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if challenge == nil {
		t.Fatal("Authenticate() returned no 2FA challenge, want one")
	}
	if len(session.Raw) != 0 {
		t.Error("Authenticate() returned a usable session before 2FA was submitted")
	}

	if _, err := challenge.Submit(context.Background(), "000000"); !errors.Is(err, apiclient.ErrInvalidCredentials) {
		t.Errorf("Submit(wrong code) error = %v, want ErrInvalidCredentials", err)
	}

	session, err = challenge.Submit(context.Background(), "123456")
	if err != nil {
		t.Fatalf("Submit(correct code) error = %v", err)
	}
	if len(session.Raw) == 0 {
		t.Error("Submit(correct code) returned an empty session")
	}
}

func TestUploadFetchRoundTrip(t *testing.T) {
	s := NewServer()
	s.AddAccount("user@example.com", "hunter2")
	session := authenticate(t, s, "user@example.com", "hunter2")
	ctx := context.Background()

	content := []byte("hello iSync")
	if err := s.Upload(ctx, session, "/Documents/notes.txt", bytesReader(content), int64(len(content))); err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	rc, err := s.Fetch(ctx, session, "/Documents/notes.txt")
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading fetched content: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("Fetch() content = %q, want %q", got, content)
	}
}

func TestUpload_CreatesImplicitParentDirs(t *testing.T) {
	s := NewServer()
	s.AddAccount("user@example.com", "hunter2")
	session := authenticate(t, s, "user@example.com", "hunter2")
	ctx := context.Background()

	content := []byte("x")
	if err := s.Upload(ctx, session, "/A/B/C/file.txt", bytesReader(content), 1); err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	items, err := s.List(ctx, session, "/A/B")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 1 || items[0].Path != "/A/B/C" || !items[0].IsDir {
		t.Errorf("List(/A/B) = %+v, want one dir /A/B/C", items)
	}

	items, err = s.List(ctx, session, "/A/B/C")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 1 || items[0].Path != "/A/B/C/file.txt" {
		t.Errorf("List(/A/B/C) = %+v, want one file", items)
	}
}

func TestList_UnknownPath(t *testing.T) {
	s := NewServer()
	s.AddAccount("user@example.com", "hunter2")
	session := authenticate(t, s, "user@example.com", "hunter2")

	if _, err := s.List(context.Background(), session, "/does/not/exist"); !errors.Is(err, apiclient.ErrNotFound) {
		t.Errorf("List() error = %v, want ErrNotFound", err)
	}
}

func TestFetch_MissingFile(t *testing.T) {
	s := NewServer()
	s.AddAccount("user@example.com", "hunter2")
	session := authenticate(t, s, "user@example.com", "hunter2")

	if _, err := s.Fetch(context.Background(), session, "/nope.txt"); !errors.Is(err, apiclient.ErrNotFound) {
		t.Errorf("Fetch() error = %v, want ErrNotFound", err)
	}
}

func TestDelete_RemovesFileAndDescendants(t *testing.T) {
	s := NewServer()
	s.AddAccount("user@example.com", "hunter2")
	session := authenticate(t, s, "user@example.com", "hunter2")
	ctx := context.Background()

	_ = s.Upload(ctx, session, "/Folder/a.txt", bytesReader([]byte("a")), 1)
	_ = s.Upload(ctx, session, "/Folder/sub/b.txt", bytesReader([]byte("b")), 1)

	if err := s.Delete(ctx, session, "/Folder"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if _, err := s.Fetch(ctx, session, "/Folder/a.txt"); !errors.Is(err, apiclient.ErrNotFound) {
		t.Errorf("Fetch(deleted file) error = %v, want ErrNotFound", err)
	}
	if _, err := s.Fetch(ctx, session, "/Folder/sub/b.txt"); !errors.Is(err, apiclient.ErrNotFound) {
		t.Errorf("Fetch(deleted nested file) error = %v, want ErrNotFound", err)
	}
}

func TestDelete_MissingPath(t *testing.T) {
	s := NewServer()
	s.AddAccount("user@example.com", "hunter2")
	session := authenticate(t, s, "user@example.com", "hunter2")

	if err := s.Delete(context.Background(), session, "/nope"); !errors.Is(err, apiclient.ErrNotFound) {
		t.Errorf("Delete() error = %v, want ErrNotFound", err)
	}
}

func TestSessionExpiry(t *testing.T) {
	s := NewServer()
	s.AddAccount("user@example.com", "hunter2")
	session := authenticate(t, s, "user@example.com", "hunter2")

	s.ExpireSession(session)

	if _, err := s.List(context.Background(), session, "/"); !errors.Is(err, apiclient.ErrSessionExpired) {
		t.Errorf("List() after expiry error = %v, want ErrSessionExpired", err)
	}
}

func TestInjectedRateLimit(t *testing.T) {
	s := NewServer()
	s.AddAccount("user@example.com", "hunter2")
	s.SetRateLimitEvery(3)

	var rateLimitedCount, okCount int
	for i := 0; i < 9; i++ {
		_, _, err := s.Authenticate(context.Background(), "user@example.com", "hunter2")
		switch {
		case errors.Is(err, apiclient.ErrRateLimited):
			rateLimitedCount++
		case err == nil:
			okCount++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if rateLimitedCount != 3 {
		t.Errorf("rateLimitedCount = %d, want 3 (every 3rd of 9 calls)", rateLimitedCount)
	}
	if okCount != 6 {
		t.Errorf("okCount = %d, want 6", okCount)
	}
}

func TestInjectedTransientFailures(t *testing.T) {
	s := NewServer()
	s.AddAccount("user@example.com", "hunter2")
	s.FailNextRequests(2)

	_, _, err := s.Authenticate(context.Background(), "user@example.com", "hunter2")
	if !errors.Is(err, apiclient.ErrTransient) {
		t.Fatalf("call 1 error = %v, want ErrTransient", err)
	}

	_, _, err = s.Authenticate(context.Background(), "user@example.com", "hunter2")
	if !errors.Is(err, apiclient.ErrTransient) {
		t.Fatalf("call 2 error = %v, want ErrTransient", err)
	}

	_, challenge, err := s.Authenticate(context.Background(), "user@example.com", "hunter2")
	if err != nil {
		t.Fatalf("call 3 error = %v, want nil (failures exhausted)", err)
	}
	if challenge != nil {
		t.Fatal("call 3 returned an unexpected 2FA challenge")
	}
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
