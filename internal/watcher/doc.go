// Package watcher detects local filesystem changes on registered folder
// pairs via fsnotify (inotify), debouncing rapid-fire events before
// signaling the sync engine. See plan.md Phase 6.
package watcher
