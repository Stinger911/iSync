// Package poller detects remote (iCloud Drive) changes via
// adaptive-interval polling, since Apple's private API exposes no
// push/webhook mechanism. The interval tightens after local or remote
// activity and backs off when idle, per INTENT.md's resolved remote
// change detection decision. See plan.md Phase 6.
package poller
