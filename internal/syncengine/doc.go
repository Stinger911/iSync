// Package syncengine holds the core diff/conflict/transfer decision
// logic: given a current local observation, current remote observation,
// and the last-known stateindex record for a path, it decides whether to
// upload, download, do nothing, resolve a last-write-wins conflict, or
// propagate a delete (routed through internal/trash). It depends only on
// the apiclient.Client interface, never a concrete implementation, so it
// is fully testable against internal/apiclient/fake. See plan.md Phase 4.
package syncengine
