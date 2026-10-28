# Spec: iSync — iCloud Drive sync client for Linux

> Derived from `INTENT.md` (status: validated). This is the next artifact
> in the chain: Intent → **Specification** → Plan → Code and tests →
> Review and deployment evidence → Incident record → New intent.

## 1. Requirements

### 1.1 Functional requirements

| ID | Requirement |
|----|-------------|
| FR-1 | User can authenticate with an Apple ID against iCloud's private web API, including interactive 2FA, via `isync login`. |
| FR-2 | User can register one or more folder pairs — an iCloud Drive path and a local filesystem path — via `isync add <icloud-path> <local-path>`, and remove/list them (`isync remove`, `isync list`). |
| FR-3 | For each registered folder pair, iSync performs full two-way sync: a file created/modified/deleted on either side is propagated to the other. |
| FR-4 | Local changes are detected via filesystem events (inotify/`fsnotify`); remote (iCloud) changes are detected via adaptive-interval polling. |
| FR-5 | Before transferring, iSync determines whether a file actually changed using a cheap size+mtime check, falling back to a content hash comparison only when that check is ambiguous — avoiding redundant transfers. |
| FR-6 | When the same file was modified on both sides between sync passes, iSync resolves the conflict automatically using last-write-wins by modification timestamp; no file is silently lost — see FR-7. |
| FR-7 | Every delete iSync performs (either direction) moves the file into a local trash directory instead of unlinking it, retained for a configurable period (default 30 days) with periodic cleanup. This is in addition to, not a replacement for, iCloud's own "Recently Deleted" retention. |
| FR-8 | Files above a configurable max-size threshold (default ~10 GiB) are never attempted; they are skipped and clearly surfaced (status/log/notification). |
| FR-9 | Transient transfer errors are retried with bounded exponential backoff; persistent/size-related failures are not retried indefinitely. |
| FR-10 | All iCloud API requests are paced adaptively (slow down on rate-limit signals, speed back up after sustained success) and bounded to a max concurrency (default ~3 simultaneous transfers). |
| FR-11 | iSync runs continuously as a systemd service (daemon mode) and also supports one-off, on-demand sync via CLI invocation. |
| FR-12 | `isync status` reports: daemon running state, per-pair last-sync time, pending/in-flight transfers, skipped-oversize files, and recent errors. |
| FR-13 | Credentials (Apple ID session/trust tokens) are stored encrypted at rest, protected by a user-supplied master password; the CLI can set/rotate this password. The daemon obtains the master password at startup via systemd `LoadCredential=`/environment, without requiring a TTY. |
| FR-14 | On session/trust-token expiry, the daemon stops attempting silently-failing syncs and surfaces a clear re-authentication prompt (CLI message / status / log), rather than looping forever. |

### 1.2 Non-functional requirements

| ID | Requirement |
|----|-------------|
| NFR-1 | **Language/runtime**: Go, compiled to a static, CGO-free binary (no dynamic linking dependency, no SQLite/cgo driver). |
| NFR-2 | **Platforms**: Linux only, amd64 and arm64, no minimum kernel/distro version beyond what a static Go binary and (for daemon mode) systemd require. |
| NFR-3 | **No telemetry.** No data about the user's files, account, or usage leaves the local machine except for calls to Apple's own API needed to perform sync. |
| NFR-4 | **License**: Apache License 2.0, `LICENSE` + `NOTICE` present at repo root (already done). |
| NFR-5 | **Testability**: the Apple API client is isolated behind an interface; all CI/automated tests run against a hand-built fake API server, never real Apple infrastructure. |
| NFR-6 | **Auditability**: credential storage format, auth flow, and sync logic must be straightforward enough for a third-party contributor to read and trust the project with their Apple credentials. |
| NFR-7 | **Resilience to API fragility**: because the integration is a reverse-engineered private API with no stability guarantee, the API client layer must be swappable/patchable without touching sync-engine logic. |
| NFR-8 | **Data safety first**: no single bug or API misbehavior should be able to silently destroy user data with no recovery path (addressed by trash/quarantine + iCloud's own retention). |

### 1.3 Explicit non-goals (v1)

- iCloud Photos, Mail, Contacts, Find My, Hide My Email, or any non-Drive iCloud service.
- Multi-account support; multi-user/shared-machine support.
- Windows/macOS builds; any GUI.
- Distro-native packages (.deb/.rpm/AUR/Nix).
- Syncing the entire iCloud Drive (sync is opt-in per folder).
- Client-side file chunking/splitting for large files (would break native iCloud Drive compatibility).

## 2. Design

### 2.1 High-level architecture

```
                        ┌──────────────────────────┐
                        │        isync CLI         │
                        │  login / add / remove /  │
                        │  list / status / sync    │
                        └────────────┬─────────────┘
                                     │ (local IPC: unix socket or direct
                                     │  shared state via BoltDB file)
                                     ▼
┌───────────────────────────────────────────────────────────────────┐
│                         isyncd (daemon)                           │
│                                                                     │
│  ┌───────────────┐   ┌────────────────┐   ┌─────────────────────┐ │
│  │ Local watcher │   │ Remote poller  │   │   Sync engine        │ │
│  │ (fsnotify)    │──▶│ (adaptive      │──▶│  - diff decision     │ │
│  │               │   │  interval)     │   │  - conflict policy   │ │
│  └───────────────┘   └────────────────┘   │  - transfer queue    │ │
│                                            │  - trash/quarantine  │ │
│                                            └──────────┬───────────┘ │
│                                                        │             │
│                                  ┌─────────────────────▼──────────┐ │
│                                  │   iCloud API client (interface) │ │
│                                  │   - auth/2FA, pacer, retries    │ │
│                                  └─────────────────────┬──────────┘ │
│                                                        │             │
│  ┌────────────────┐   ┌───────────────┐   ┌───────────▼──────────┐ │
│  │ Credential store│  │  State index  │   │  Apple private web   │ │
│  │ (age, scrypt)   │  │  (BoltDB)     │   │  API (real / fake in │ │
│  └────────────────┘   └───────────────┘   │  tests)               │ │
│                                            └───────────────────────┘ │
└───────────────────────────────────────────────────────────────────┘
```

### 2.2 Components

**CLI (`isync`)**
- Subcommands: `login`, `logout`, `add <icloud-path> <local-path>`, `remove <pair-id>`, `list`, `status`, `sync [pair-id]` (one-off), `daemon` (runs in foreground, for systemd `ExecStart`).
- Talks to the running daemon (if any) for live status; otherwise operates directly on the BoltDB state + credential store for one-off/offline commands.

**Local watcher**
- Uses `fsnotify` (inotify backend) on each registered local folder pair, recursively.
- Debounces rapid-fire events (e.g. editors doing write+rename) before enqueuing a sync decision.
- On detecting activity, also signals the remote poller to tighten its interval (shared "activity" signal, per FR-4/the adaptive-polling design).

**Remote poller**
- Periodically lists each registered iCloud Drive folder pair's contents via the API client.
- Adaptive interval: baseline ~60s → tightens to ~10–15s after activity (local or remote) → backs off to ~5–10 min when idle.
- Also tied into the rate-limit pacer: a sustained rate-limit signal extends the polling interval independently of the idle/activity logic.

**Sync engine**
- Central diffing/decision logic, shared regardless of which side (local watcher or remote poller) triggered the check.
- For a given path: compare state-index record (last-known local mtime/size/hash, last-known remote etag/modified-date) against current local and remote observations.
  - If only one side changed since last sync → propagate that change.
  - If both sides changed → conflict → apply last-write-wins by timestamp, loser's prior content preserved via the normal delete-to-trash path.
  - If a delete is detected on one side → propagate delete-with-trash to the other side (FR-7).
- Emits transfer jobs (upload/download/delete) to a bounded worker pool (concurrency cap, FR-10).
- Skips/flags files over the max-size threshold before ever attempting a transfer (FR-8).

**iCloud API client**
- Implements a narrow internal interface (`Lister`, `Fetcher`, `Uploader`, `Deleter`, `Authenticator`) so the sync engine never talks to raw HTTP directly.
- Wraps auth (incl. interactive 2FA), the adaptive pacer (FR-10), and bounded retry-with-backoff (FR-9).
- Two implementations: the real client (talks to Apple's private web API, modeled after pyicloud/rclone's `iclouddrive`/icloudgo) and a fake server implementation used in tests (NFR-5).

**Credential store**
- `~/.config/isync/credentials.age` — JSON blob (Apple ID, session cookie, trust token) encrypted via `age` with a scrypt-derived passphrase identity.
- CLI (`isync login`) writes/rotates it interactively (prompts for Apple ID password + 2FA code + master password).
- Daemon obtains the master password via systemd `LoadCredential=`/env at service start; decrypted material stays in memory only for the daemon process's lifetime.

**State index**
- Embedded BoltDB (`etcd-io/bbolt`) file, e.g. `~/.local/state/isync/state.db`.
- Buckets: one per folder pair, keyed by relative path, storing `{local_mtime, local_size, local_hash, remote_etag, remote_modified, last_synced_at}`.
- Source of truth for "is this file already in sync" — avoids spurious transfers from clock skew alone.

**Trash/quarantine**
- `~/.local/share/isync/trash/<YYYY-MM-DDTHH-MM>/<relative-path>`; on same-minute path collision, the colliding entry's filename gets the full timestamp (incl. seconds/sub-second) appended to stay unique.
- Background cleanup job (run periodically by the daemon) purges entries older than the configured retention (default 30 days).

### 2.3 Configuration

- Format: TOML, at `~/.config/isync/config.toml`.
- Example shape:
```toml
[daemon]
poll_interval_idle = "5m"
poll_interval_active = "15s"
poll_interval_baseline = "60s"
max_concurrent_transfers = 3
max_file_size = "10GiB"
trash_retention_days = 30

[[pairs]]
id = "documents"
icloud_path = "/Documents"
local_path = "/home/user/iCloudDocuments"
```

### 2.4 Error handling & observability

- Structured logging (level-tagged) to stdout/journald when run under systemd.
- `isync status` surfaces: auth state, per-pair sync lag, in-flight/queued transfers, skipped-oversize files, recent errors, current pacer delay (diagnostic for rate-limit pressure).
- Re-auth required (expired session/trust token) is a distinct, clearly labeled state — never silently retried forever.

### 2.5 Testing architecture

- Fake API server (`internal/fakeapi` or similar): implements auth/2FA challenge-response, listing, upload, download, delete, and can be configured to inject rate-limit responses, transient errors, and session-expiry events.
- Unit tests for: diff/conflict logic, pacer backoff/decay behavior, trash path collision handling, max-file-size gating, credential encrypt/decrypt round-trip.
- Integration tests: full sync-engine runs against the fake API server, including multi-pass scenarios (conflict, delete, large-file skip).
- Manual pre-release checklist: real login/2FA, a small real two-way sync against a throwaway Apple ID, verifying both directions and a delete.

## 3. Open items for the next stage (`plan.md`)

These are implementation-level details intentionally left for planning, not spec:

- Exact Go module/package layout (`cmd/isync`, `cmd/isyncd`, `internal/...`).
- IPC mechanism between CLI and daemon for live status (unix socket vs. reading the same BoltDB file).
- Exact set of Apple private API endpoints to implement first (auth/2FA, drive listing, upload, download, delete) and in what order, with reference to rclone's `iclouddrive` backend and `icloudgo`/pyicloud source as implementation guides.
- CI pipeline setup (GitHub Actions: build matrix for amd64/arm64, fake-server test suite, lint).
- systemd unit file template content and install script mechanics.
