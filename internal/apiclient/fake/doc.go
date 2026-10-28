// Package fake implements apiclient.Client as an in-memory fake server,
// with injectable behaviors (rate-limit responses, transient errors,
// session expiry, 2FA challenges) so all automated tests can run without
// touching real Apple infrastructure. See spec.md §2.5 and plan.md Phase 2.
package fake
