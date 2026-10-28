// Package ipc provides the communication mechanism between the isync CLI
// and the running isyncd daemon for live status queries and one-off sync
// triggers (e.g. a daemon-written status snapshot file, per plan.md's
// note to avoid over-engineering this for v1). See plan.md Phase 7.
package ipc
