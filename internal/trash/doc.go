// Package trash implements iSync's delete safety net: every delete the
// sync engine performs is moved into a local quarantine directory
// (~/.local/share/isync/trash/<minute-bucket>/...) instead of being
// unlinked, with collision-safe naming and periodic retention cleanup,
// per INTENT.md's resolved delete-propagation decision. See plan.md
// Phase 5.
package trash
