// Package auth implements the login flow on top of apiclient.Client:
// driving the Apple ID + password + interactive 2FA exchange and handing
// the resulting session to internal/credstore for persistence. See
// plan.md Phase 3.
package auth
