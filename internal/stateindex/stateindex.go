package stateindex

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Record is the last-known, in-sync state for one path within a folder
// pair. It is the sync engine's source of truth for "already in sync".
// The shape matches spec.md §2.2.
type Record struct {
	LocalModified  time.Time `json:"local_modified"`
	LocalSize      int64     `json:"local_size"`
	LocalHash      string    `json:"local_hash,omitempty"`
	RemoteModified time.Time `json:"remote_modified"`
	RemoteETag     string    `json:"remote_etag,omitempty"`
	LastSyncedAt   time.Time `json:"last_synced_at"`
}

// ErrNotFound is returned by Get when no record exists for a pair+path.
var ErrNotFound = errors.New("stateindex: record not found")

// Store is a BoltDB-backed, per-folder-pair state index. It is safe for
// concurrent use by multiple goroutines (BoltDB serializes writers).
type Store struct {
	db *bolt.DB
}

// Open opens (creating if necessary) the state database at path. Callers
// must call Close when done. See plan.md Phase 4.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return nil, fmt.Errorf("stateindex: opening %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// bucket returns the BoltDB bucket name for a folder-pair id.
func bucket(pairID string) []byte { return []byte("pair:" + pairID) }

// Get returns the stored record for (pairID, relPath), or ErrNotFound.
func (s *Store) Get(pairID, relPath string) (Record, error) {
	var record Record
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket(pairID))
		if b == nil {
			return ErrNotFound
		}
		raw := b.Get([]byte(relPath))
		if raw == nil {
			return ErrNotFound
		}
		return json.Unmarshal(raw, &record)
	})
	return record, err
}

// Put stores record under (pairID, relPath), creating the pair bucket on
// first use.
func (s *Store) Put(pairID, relPath string, record Record) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("stateindex: encoding record: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucket(pairID))
		if err != nil {
			return err
		}
		return b.Put([]byte(relPath), raw)
	})
}

// Delete removes the record for (pairID, relPath). It is a no-op if the
// pair bucket or record does not exist.
func (s *Store) Delete(pairID, relPath string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket(pairID))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(relPath))
	})
}

// ForEach invokes fn for every (relPath, record) in the given pair, in
// lexicographic key order. If fn returns an error, iteration stops and
// that error is returned.
func (s *Store) ForEach(pairID string, fn func(relPath string, record Record) error) error {
	return s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket(pairID))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var record Record
			if err := json.Unmarshal(v, &record); err != nil {
				return fmt.Errorf("stateindex: decoding record %q: %w", string(k), err)
			}
			return fn(string(k), record)
		})
	})
}
