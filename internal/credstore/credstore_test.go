package credstore

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveLoad_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "credentials.age")

	creds := Credentials{
		AppleID:       "user@example.com",
		SessionCookie: "cookie-value",
		TrustToken:    "trust-token-value",
		Extra:         map[string]string{"scnt": "abc123"},
	}

	if err := Save(path, "correct horse battery staple", creds); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := Load(path, "correct horse battery staple")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.AppleID != creds.AppleID || got.SessionCookie != creds.SessionCookie || got.TrustToken != creds.TrustToken {
		t.Errorf("Load() = %+v, want %+v", got, creds)
	}
	if got.Extra["scnt"] != "abc123" {
		t.Errorf("Extra[scnt] = %q, want %q", got.Extra["scnt"], "abc123")
	}
}

func TestLoad_WrongPassphrase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.age")

	creds := Credentials{AppleID: "user@example.com"}
	if err := Save(path, "correct-password", creds); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	_, err := Load(path, "wrong-password")
	if !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("Load() error = %v, want ErrWrongPassphrase", err)
	}
}

func TestSave_FilePermissions(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("permission bits test is Linux-specific")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "credentials.age")

	if err := Save(path, "pw", Credentials{AppleID: "a@b.com"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 0600", perm)
	}

	di, err := os.Stat(filepath.Join(dir, "nested"))
	if err != nil {
		t.Fatalf("Stat(dir) error = %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir mode = %o, want 0700", perm)
	}
}

func TestSave_EmptyPassphraseRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.age")
	if err := Save(path, "", Credentials{AppleID: "a@b.com"}); err == nil {
		t.Error("Save() with empty passphrase = nil error, want error")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.age")
	if _, err := Load(path, "pw"); err == nil {
		t.Error("Load() on missing file = nil error, want error")
	}
}

func TestExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.age")
	if Exists(path) {
		t.Error("Exists() = true before Save, want false")
	}
	if err := Save(path, "pw", Credentials{AppleID: "a@b.com"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if !Exists(path) {
		t.Error("Exists() = false after Save, want true")
	}
}

func TestRotate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.age")
	creds := Credentials{AppleID: "user@example.com", SessionCookie: "c1"}

	if err := Save(path, "old-pw", creds); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if err := Rotate(path, "old-pw", "new-pw"); err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}

	if _, err := Load(path, "old-pw"); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("Load() with old passphrase after rotate error = %v, want ErrWrongPassphrase", err)
	}

	got, err := Load(path, "new-pw")
	if err != nil {
		t.Fatalf("Load() with new passphrase error = %v", err)
	}
	if got.AppleID != creds.AppleID {
		t.Errorf("AppleID = %q, want %q", got.AppleID, creds.AppleID)
	}
}

func TestRotate_WrongOldPassphraseLeavesFileUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.age")
	creds := Credentials{AppleID: "user@example.com"}

	if err := Save(path, "old-pw", creds); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if err := Rotate(path, "wrong-pw", "new-pw"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("Rotate() error = %v, want ErrWrongPassphrase", err)
	}

	// Original passphrase should still work.
	if _, err := Load(path, "old-pw"); err != nil {
		t.Errorf("Load() with original passphrase after failed rotate error = %v, want nil", err)
	}
}
