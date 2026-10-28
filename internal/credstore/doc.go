// Package credstore reads, writes, and rotates iSync's encrypted
// credential file (Apple ID, session cookie, trust token), using age
// (filippo.io/age) with a scrypt-derived passphrase identity, per
// INTENT.md's resolved credential-file-crypto decision. See plan.md
// Phase 1.
package credstore
