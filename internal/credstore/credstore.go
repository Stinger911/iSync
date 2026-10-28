// Package credstore reads, writes, and rotates iSync's encrypted
// credential file (Apple ID, session cookie, trust token), using age
// (filippo.io/age) with a scrypt-derived passphrase identity, per
// INTENT.md's resolved credential-file-crypto decision. See plan.md
// Phase 1.
package credstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"filippo.io/age"
)

// Credentials holds whatever iSync needs to resume an authenticated
// iCloud session without a fresh interactive login/2FA: the Apple ID,
// the session cookie, and the trust token established on first login.
// Extra, implementation-specific values the real API client needs can be
// carried in Extra without changing the on-disk schema.
type Credentials struct {
	AppleID       string            `json:"apple_id"`
	SessionCookie string            `json:"session_cookie"`
	TrustToken    string            `json:"trust_token"`
	Extra         map[string]string `json:"extra,omitempty"`
}

// ErrWrongPassphrase is returned by Load when decryption fails because the
// supplied master password doesn't match the one the file was encrypted
// with (as opposed to a missing/corrupt file).
var ErrWrongPassphrase = errors.New("credstore: wrong master password")

// DefaultPath returns the conventional credential file location,
// ~/.config/isync/credentials.age, honoring $XDG_CONFIG_HOME.
func DefaultPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("credstore: resolving home directory: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "isync", "credentials.age"), nil
}

// Exists reports whether a credential file is already present at path.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Save encrypts creds with passphrase and writes it to path (mode 0600),
// creating parent directories (mode 0700) as needed. Any existing file at
// path is overwritten — callers rotating a password should decrypt with
// the old passphrase first, then Save with the new one.
func Save(path, passphrase string, creds Credentials) error {
	if passphrase == "" {
		return errors.New("credstore: passphrase must not be empty")
	}

	plaintext, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("credstore: encoding credentials: %w", err)
	}

	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return fmt.Errorf("credstore: deriving encryption key: %w", err)
	}

	var ciphertext bytes.Buffer
	w, err := age.Encrypt(&ciphertext, recipient)
	if err != nil {
		return fmt.Errorf("credstore: starting encryption: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		return fmt.Errorf("credstore: encrypting: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("credstore: finalizing encryption: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("credstore: creating directory for %s: %w", path, err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, ciphertext.Bytes(), 0o600); err != nil {
		return fmt.Errorf("credstore: writing %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("credstore: finalizing %s: %w", path, err)
	}

	return nil
}

// Load decrypts the credential file at path using passphrase.
func Load(path, passphrase string) (Credentials, error) {
	if passphrase == "" {
		return Credentials{}, errors.New("credstore: passphrase must not be empty")
	}

	ciphertext, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("credstore: reading %s: %w", path, err)
	}

	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return Credentials{}, fmt.Errorf("credstore: deriving decryption key: %w", err)
	}

	r, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return Credentials{}, ErrWrongPassphrase
		}
		return Credentials{}, fmt.Errorf("credstore: decrypting %s: %w", path, err)
	}

	plaintext, err := io.ReadAll(r)
	if err != nil {
		return Credentials{}, fmt.Errorf("credstore: reading decrypted content: %w", err)
	}

	var creds Credentials
	if err := json.Unmarshal(plaintext, &creds); err != nil {
		return Credentials{}, fmt.Errorf("credstore: decoding credentials: %w", err)
	}

	return creds, nil
}

// Rotate re-encrypts the credential file at path under newPassphrase,
// first verifying oldPassphrase decrypts it successfully. The file is
// left untouched if any step fails.
func Rotate(path, oldPassphrase, newPassphrase string) error {
	creds, err := Load(path, oldPassphrase)
	if err != nil {
		return err
	}
	return Save(path, newPassphrase, creds)
}
