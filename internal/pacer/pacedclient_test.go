package pacer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Stinger911/iSync/internal/apiclient"
	"github.com/Stinger911/iSync/internal/apiclient/fake"
)

func newTestPolicy(maxAttempts int) Policy {
	return Policy{
		Pacer:       New(Config{MinDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}),
		Limiter:     NewLimiter(3),
		MaxAttempts: maxAttempts,
	}
}

func TestWrap_RetriesRateLimitedUntilSuccess(t *testing.T) {
	backend := fake.NewServer()
	backend.AddAccount("user@example.com", "hunter2")
	backend.SetRateLimitEvery(2) // every 2nd call fails

	client := Wrap(backend, newTestPolicy(5))

	// Authenticate is one call per attempt; the wrapper should retry
	// through the injected rate limit until it succeeds.
	_, challenge, err := client.Authenticate(context.Background(), "user@example.com", "hunter2")
	if err != nil {
		t.Fatalf("Authenticate() error = %v, want nil (should retry through rate limit)", err)
	}
	if challenge != nil {
		t.Fatal("Authenticate() returned an unexpected 2FA challenge")
	}
}

func TestWrap_GivesUpAfterMaxAttempts(t *testing.T) {
	backend := fake.NewServer()
	backend.AddAccount("user@example.com", "hunter2")
	backend.FailNextRequests(10) // far more than MaxAttempts

	client := Wrap(backend, newTestPolicy(3))

	_, _, err := client.Authenticate(context.Background(), "user@example.com", "hunter2")
	if !errors.Is(err, apiclient.ErrTransient) {
		t.Errorf("Authenticate() error = %v, want ErrTransient after exhausting retries", err)
	}
}

func TestWrap_DoesNotRetryNonRetryableErrors(t *testing.T) {
	backend := fake.NewServer()
	backend.AddAccount("user@example.com", "hunter2")

	client := Wrap(backend, newTestPolicy(5))

	_, _, err := client.Authenticate(context.Background(), "user@example.com", "wrong-password")
	if !errors.Is(err, apiclient.ErrInvalidCredentials) {
		t.Errorf("Authenticate() error = %v, want ErrInvalidCredentials (not retried)", err)
	}
}

func TestWrap_PassesThroughSuccessfulOperations(t *testing.T) {
	backend := fake.NewServer()
	backend.AddAccount("user@example.com", "hunter2")

	client := Wrap(backend, newTestPolicy(5))
	ctx := context.Background()

	session, _, err := client.Authenticate(ctx, "user@example.com", "hunter2")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}

	content := []byte("hello")
	if err := client.Upload(ctx, session, "/a.txt", stringsReader(content), int64(len(content))); err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	items, err := client.List(ctx, session, "/")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 1 || items[0].Path != "/a.txt" {
		t.Errorf("List() = %+v, want one item /a.txt", items)
	}

	if err := client.Delete(ctx, session, "/a.txt"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestWrap_RetriesRewindableUpload(t *testing.T) {
	backend := fake.NewServer()
	backend.AddAccount("user@example.com", "hunter2")
	client := Wrap(backend, newTestPolicy(3))
	ctx := context.Background()
	session, _, err := client.Authenticate(ctx, "user@example.com", "hunter2")
	if err != nil {
		t.Fatalf("Authenticate(): %v", err)
	}

	backend.FailNextRequests(1)
	payload := []byte("retry me")
	if err := client.Upload(ctx, session, "/retry.txt", bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("Upload() with a rewindable reader: %v", err)
	}
}

func TestWrap_DoesNotRetryOneShotUpload(t *testing.T) {
	backend := fake.NewServer()
	backend.AddAccount("user@example.com", "hunter2")
	client := Wrap(backend, newTestPolicy(3))
	ctx := context.Background()
	session, _, err := client.Authenticate(ctx, "user@example.com", "hunter2")
	if err != nil {
		t.Fatalf("Authenticate(): %v", err)
	}

	backend.FailNextRequests(1)
	payload := []byte("one shot")
	err = client.Upload(ctx, session, "/one-shot.txt", stringsReader(payload), int64(len(payload)))
	if !errors.Is(err, apiclient.ErrTransient) {
		t.Fatalf("Upload() with a one-shot reader error = %v, want one unretried ErrTransient", err)
	}
}

func TestWrap_LimiterBoundsConcurrentCalls(t *testing.T) {
	backend := fake.NewServer()
	backend.AddAccount("user@example.com", "hunter2")

	policy := Policy{Limiter: NewLimiter(1), MaxAttempts: 1}
	client := Wrap(backend, policy)
	ctx := context.Background()

	session, _, err := client.Authenticate(ctx, "user@example.com", "hunter2")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}

	done := make(chan struct{})
	started := make(chan struct{})
	go func() {
		close(started)
		_, _ = client.List(ctx, session, "/")
		close(done)
	}()
	<-started

	// With Limiter capacity 1, a concurrent List should still complete
	// (serialized, not deadlocked) within a reasonable time.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent List() under a size-1 limiter never completed")
	}
}

func stringsReader(b []byte) *stringsReaderT { return &stringsReaderT{b: b} }

type stringsReaderT struct {
	b   []byte
	pos int
}

func (r *stringsReaderT) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}
