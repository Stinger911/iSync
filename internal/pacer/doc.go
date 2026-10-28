// Package pacer implements the adaptive request pacer and bounded
// concurrency limiter shared by internal/apiclient/real, per INTENT.md's
// resolved rate-limiting strategy: a baseline inter-request delay that
// multiplies on rate-limit signals and decays after sustained success,
// combined with a max-concurrent-transfers cap. See plan.md Phase 3.
package pacer
