# Intent: iSync — iCloud Drive sync client for Linux

## Problem

Apple provides no official iCloud Drive client for Linux. Users who rely on
iCloud Drive on macOS/iOS have no supported way to keep a local folder on a
Linux machine in sync with it. Existing workarounds are either manual
(logging into icloud.com and dragging files), require owning a Mac/Windows
box as a bridge, or depend on third-party paid services. People who run
Linux as a daily driver but remain in the Apple ecosystem for phone/photos
need a first-class, local, automatic way to keep chosen iCloud Drive folders
mirrored on their Linux filesystem.

## Proposed outcome

`iSync` is a Go CLI + background daemon that performs **full two-way sync**
between one or more **user-selected iCloud Drive folders** and corresponding
local directories on Linux:

- Changes made on either side (locally or in iCloud Drive, from any device)
  are detected and propagated to the other side without manual intervention.
- Runs as a `systemd` user/system service for continuous background sync,
  and exposes a CLI for one-off syncs, status checks, login/auth setup, and
  folder-pair management.
- Conflicts (same file changed on both sides between sync passes) are
  resolved automatically using **last-write-wins by modification timestamp**.
- Authenticates to iCloud using the same reverse-engineered private web API
  approach used by projects like `pyicloud` / rclone's `iclouddrive` backend,
  including interactive Apple ID two-factor authentication on first login.
- Session/trust tokens and any stored secrets are kept in an **encrypted
  local file protected by a master password** (not the OS keyring), so the
  tool has no hard dependency on a desktop secret-service daemon.

Definition of done for v1: a user can `isync login`, `isync add <icloud-path>
<local-path>`, start the daemon, edit a file on either side, and see it
converge on the other side within one sync interval — survives a restart and
a token refresh without manual re-auth.

## Affected users & systems

- **Primary user**: a single individual running Linux desktop/server who has
  an active Apple ID with iCloud Drive content, syncing between one personal
  Linux machine and their iCloud account (which may also be accessed from
  Mac/iOS devices concurrently — those are "the other side" of the sync,
  not directly controlled by iSync).
- **Systems**: Apple's undocumented iCloud web API (api/setup.icloud.com and
  friends), the local Linux filesystem, systemd (service supervision), and
  whatever local secret storage mechanism v1 ships with (encrypted file).
- Not in scope for v1: iCloud Photos, Mail, Contacts, Find My, Hide My Email,
  or any other iCloud service — **iCloud Drive files/folders only**.
- Not in scope for v1: multi-account support, multi-user/shared-machine
  support, Windows/macOS builds (Linux only), or a GUI.

## Constraints

- **Language: Go.** No official Apple API exists for either language; the
  decision is based on ecosystem maturity for *this specific problem*:
  `rclone` ships and maintains a production-grade `iclouddrive` backend in
  Go (`rclone/rclone/backend/iclouddrive`), proven across its large user
  base, and `icloudgo` (`chyroc/icloudgo`, a Go port of pyicloud) is a
  functioning open-source reference for Drive/Photos access and 2FA.
  Rust's options (`opendal`'s `services-icloud`, `icloud-fuse`,
  `apple-private-apis`) are comparatively earlier-stage/experimental. Go
  also gives easy static binaries and a straightforward systemd/CLI story.
- No official, documented, or supported Apple API — this is a reverse-
  engineered private API integration. It can break without notice on
  Apple-side changes and sits in a legal/ToS grey area; this must be
  clearly disclosed to users (README/disclaimer), and the architecture
  should isolate the API client behind an interface so it can be patched
  or swapped without touching sync logic.
- Sync scope is **opt-in per folder**, not the whole Drive, to limit
  blast radius, API load, and local disk usage.
- Must survive interactive 2FA at login and handle session/trust-token
  expiry by prompting for re-auth rather than silently failing forever.
- Open source project (public GitHub repo) under the **Apache License 2.0**
  — chosen over MIT for its explicit patent grant/retaliation clause and
  clean contribution provenance (relevant given the reverse-engineered API
  and an eventual expectation of outside contributors). Code, auth flow,
  and storage format must be sane for third parties to read, audit, and
  trust with their Apple credentials — no telemetry, no secrets leaving
  the local machine.
- No fixed deadline; this is an exploratory/learning project, so
  correctness and a clean, learnable codebase matter more than speed of
  delivery.

## Open questions

- ~~Credential-file crypto~~ **Resolved**: credentials (Apple ID, session
  cookie, trust token) are serialized (JSON) and encrypted as a single blob
  using `age` (`filippo.io/age`) with a passphrase-derived (`scrypt`)
  recipient — chosen over hand-rolled NaCl/AES schemes to avoid owning a
  custom file format/nonce handling, in favor of a reviewed, versioned,
  widely-deployed format. Stored at `~/.config/isync/credentials.age`,
  mode `0600`. The CLI (`isync login`) writes/rotates it interactively.
  The daemon, having no TTY, receives the master password via systemd
  `LoadCredential=`/environment set up by the user when starting the
  service; decrypted material lives only in process memory for the
  daemon's lifetime and is re-requested on 2FA/session expiry.
- ~~Remote change detection~~ **Resolved**: hybrid detection. Local-side
  changes are detected instantly via filesystem watch (`fsnotify`, inotify
  under Linux) — no local polling needed. Remote-side (iCloud Drive) changes
  are detected via **adaptive-interval polling**, since Apple's private API
  exposes no public push/webhook mechanism: a baseline interval (default
  ~60s) tightens to ~10–15s for a short window after any detected activity
  (local or remote), and backs off to ~5–10 min when idle. This balances
  responsiveness against Apple API load/rate-limit risk without the much
  higher reverse-engineering cost of chasing Apple's undocumented internal
  push/long-poll endpoints.
- ~~Metadata mapping / change-detection strategy~~ **Resolved**: hybrid
  size+mtime/hash comparison. Local size+mtime and iCloud's reported
  size+modified-date act as a cheap first-pass filter to skip files that
  are obviously unchanged; a content hash (SHA-256 locally, compared
  against Apple's item etag/checksum where available) is computed only
  when size or mtime differ, to avoid spurious transfers from clock skew
  or no-op touches. This state — per-path local mtime/size/hash and
  remote etag/modified-date — is persisted in an embedded **BoltDB
  (`etcd-io/bbolt`)** key-value store as the sync engine's source of
  truth for "already in sync", chosen over SQLite to keep the project
  CGO-free (easier static cross-compilation, matching the Go/systemd
  single-binary story).
- ~~Delete propagation / safety net~~ **Resolved**: deletes propagate
  (matches 2-way sync expectations), but every delete isync performs —
  regardless of which side triggered it — moves the file into a **local
  trash/quarantine** at `~/.local/share/isync/trash/<YYYY-MM-DDTHH-MM>/...`
  instead of unlinking it, with a configurable retention period (default
  ~30 days) and periodic cleanup. The trash directory is keyed to the
  **minute** of the delete; if a second delete lands in the same minute
  and would collide on path, the colliding entry's filename gets the
  **full timestamp (down to the second/sub-second) appended** to stay
  unique. This local trash is combined with, not a replacement for,
  **iCloud's own built-in "Recently Deleted" retention** on the remote
  side — giving one consistent safety net for local→remote deletes plus
  Apple's existing mechanism as a second line of defense for remote-side
  deletes, important given the whole integration rests on an undocumented
  private API where "is this really a delete or an API glitch" deserves
  real insurance.
- ~~Large file / chunked upload support~~ **Resolved**: no client-side
  chunking/splitting — it would make large files unreadable as normal
  files to other Apple devices (Mac/iPhone), defeating the goal of staying
  interoperable with the real iCloud Drive ecosystem. The private web API
  iSync depends on only supports single-shot uploads and empirically fails
  (`400 Bad Request`) around ~10GB, well under Apple's documented 50GB
  per-file cap. Instead: a **configurable max-file-size threshold**
  (default ~10 GiB) is enforced client-side — files under it sync normally
  with bounded exponential-backoff retry on transient errors; files over
  it are **not attempted**, and are skipped with a clear, visible flag
  (`isync status`, logs, optional notification) rather than retried
  indefinitely against a known hard wall.
- ~~Rate limiting / throttling strategy~~ **Resolved**: an **adaptive
  pacer** (rclone-`iclouddrive`-style) — a baseline inter-request delay
  that multiplies (e.g. ×2, capped) on any rate-limit signal (HTTP 429, or
  the `400`/`503` patterns Apple's API is known to return under load) and
  gradually decays back down after a run of successes. This mirrors a
  proven approach already validated against this exact private API,
  rather than guessing at static limits. Paired with a **bounded
  concurrency cap** (default ~3 simultaneous transfers) as an independent
  control, since parallel-connection bursts are a separate abuse signal
  from request rate. Composes with the already-decided adaptive polling
  interval — both tighten/back off together under load or idle.
- ~~Distro support & packaging story~~ **Resolved**: static Go binary
  (CGO-free, per existing constraints) for amd64/arm64 distributed via
  GitHub Releases plus `go install` support, a provided systemd user-unit
  template, and a small install script/`make install` target. No
  distro-specific packages (.deb/.rpm/AUR/Nix) for v1 — disproportionate
  maintenance burden for a solo, no-deadline, exploratory project;
  revisit if/when outside users or contributors ask for it. No hard
  minimum kernel/distro version — a static, CGO-free Go binary runs on
  essentially any modern Linux; the only real requirement is `systemd`
  present for daemon mode.
- ~~Testing strategy~~ **Resolved**: a hand-built fake API server
  implementing the subset of Apple's private endpoints iSync uses (auth,
  listing, upload, download, delete — informed by rclone/pyicloud as
  reference implementations) backs all automated unit/integration tests
  and CI, with the real API client isolated behind an interface so tests
  never touch real Apple infrastructure, and error conditions (429s,
  400s, session expiry, 2FA challenges) can be simulated on demand.
  This is paired with an occasional **manual pre-release smoke test**
  against a real throwaway Apple ID to catch real-world API drift the
  fake server can't. Record/replay (VCR-style) fixtures were considered
  and rejected — the fixture-sanitization risk of capturing real account
  traffic isn't worth the marginal fidelity gain over a well-built fake
  server for a solo project.
- Module path (`github.com/Stinger911/iSync` assumed, to confirm once
  development starts).
- Config format for folder pairs and daemon settings (YAML vs TOML).

## Author & status

- Author: Stinger911 (originator & reviewer)
- Status: **draft** — pending review before moving to `spec.md`
