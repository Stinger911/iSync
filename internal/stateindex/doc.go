// Package stateindex persists, per registered folder pair, the last-known
// local (mtime/size/hash) and remote (etag/modified-date) observations for
// each synced path, in an embedded BoltDB (etcd-io/bbolt) store. This is
// the sync engine's source of truth for "already in sync". See plan.md
// Phase 4.
package stateindex
