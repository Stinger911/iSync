# Plan: iSync — implementation plan

> Derived from `spec.md` (requirements + design), which is derived from
> validated `INTENT.md`. This artifact should be sufficient for an
> engineer or coding agent to start implementing without re-reading
> intent.md/spec.md. Chain: Intent → Specification → **Plan** → Code and
> tests → Review and deployment evidence → Incident record → New intent.

## 1. Module & package layout

```
github.com/Stinger911/iSync
├── cmd/
│   ├── isync/            # CLI entrypoint (login, add, remove, list, status, sync)
│   └── isyncd/           # daemon entrypoint (systemd ExecStart target)
├── internal/
│   ├── apiclient/        # iCloud private API client — interface + real impl
│   │   ├── client.go     #   Authenticator/Lister/Fetcher/Uploader/Deleter interfaces
│   │   ├── real/         #   HTTP implementation against Apple's private web API
│   │   └── fake/         #   in-memory fake server for tests (NFR-5)
│   ├── auth/             # login flow, 2FA challenge handling, session refresh
│   ├── credstore/        # age-encrypted credential file read/write/rotate
│   ├── stateindex/       # BoltDB-backed per-pair path state
│   ├── watcher/          # fsnotify-based local change detection + debounce
│   ├── poller/           # adaptive-interval remote polling
│   ├── pacer/            # adaptive request pacer + bounded concurrency (shared by apiclient)
│   ├── syncengine/        # diff/conflict/transfer decision logic (core, API-client-agnostic)
│   ├── trash/            # quarantine-on-delete + retention cleanup
│   ├── config/           # TOML config load/validate
│   └── ipc/              # CLI<->daemon communication (status queries, trigger sync)
├── systemd/
│   └── isyncd.service    # unit file template (user service)
├── scripts/
│   └── install.sh        # build + install binary, unit file, XDG dirs
├── LICENSE, NOTICE, README.md, INTENT.md, spec.md, plan.md
├── go.mod / go.sum
└── .github/workflows/ci.yml
```

Rationale: `internal/apiclient` isolates the fragile reverse-engineered
integration behind an interface (Constraint from INTENT.md); `syncengine`
never imports `apiclient/real` directly, only the interface, so tests run
entirely against `apiclient/fake`.

## 2. Work order (phased, each phase independently testable)

### Phase 0 — project scaffolding
- `go mod init github.com/Stinger911/iSync`, directory layout above, CI skeleton (build + `go vet` + `go test` on push/PR, matrix: linux/amd64, linux/arm64).
- `internal/config`: TOML load/validate (schema from spec.md §2.3), with defaults for all daemon settings.
- **Done when**: `go build ./...` succeeds, CI green on an empty-but-structured repo.

### Phase 1 — credential store
- `internal/credstore`: `age`-based encrypt/decrypt of a JSON `{apple_id, session_cookie, trust_token}` struct, scrypt passphrase identity, file at `~/.config/isync/credentials.age` mode `0600`.
- Unit tests: round-trip encrypt/decrypt, wrong-password failure, file permission check.
- **Done when**: a CLI stub `isync login --dry-run` can write and re-read a test credential blob end-to-end.

### Phase 2 — fake API server + client interface
- Define the `apiclient` interface: `Authenticate(creds) (Session, error)`, `List(path) ([]Item, error)`, `Fetch(path) (io.ReadCloser, error)`, `Upload(path, io.Reader) error`, `Delete(path) error`, plus 2FA challenge/response types.
- `internal/apiclient/fake`: in-memory implementation satisfying the interface, with injectable behaviors (rate-limit responses, transient errors, session expiry, 2FA challenge).
- **Done when**: a trivial program can authenticate, list, upload, download, and delete against the fake server with no real network calls.

### Phase 3 — real API client (minimum viable)
- `internal/apiclient/real`: implement auth (incl. interactive 2FA) and basic list/fetch/upload/delete against Apple's private web API, using pyicloud / rclone's `iclouddrive` backend / `icloudgo` as reference for endpoint shapes and headers.
- Wrap with `internal/pacer` (adaptive delay, bounded concurrency) and bounded retry-with-backoff (transient errors only).
- **Done when**: manual smoke test against a real throwaway Apple ID succeeds for auth + list + small file upload/download/delete (per spec.md §2.5 manual checklist — this is the first time real Apple infra is touched).

### Phase 4 — state index & diffing
- `internal/stateindex`: BoltDB wrapper, one bucket per folder pair, record shape from spec.md §2.2 (local mtime/size/hash, remote etag/modified-date, last_synced_at).
- `internal/syncengine` core diff logic: given current local observation + current remote observation + last-known state record, decide `{no-op, upload, download, conflict-lww, delete-remote, delete-local}`.
- Unit tests covering every branch: no-op (hash match), local-only change, remote-only change, both-changed conflict (LWW), delete detected either side, size+mtime ambiguous → hash fallback.
- **Done when**: diff logic has full branch coverage against table-driven test cases, no I/O (pure function over structs) so it's fast and exhaustively testable.

### Phase 5 — trash/quarantine
- `internal/trash`: on any delete the sync engine performs, move (not unlink) into `~/.local/share/isync/trash/<YYYY-MM-DDTHH-MM>/<relative-path>`; on collision within the same minute, rename using full timestamp (incl. seconds/sub-second).
- Background cleanup: periodic sweep removing entries older than `trash_retention_days` (config, default 30).
- Unit tests: collision handling (two deletes same path same minute), retention sweep boundary (file exactly at cutoff).
- **Done when**: syncengine's delete path routes through `trash` exclusively — no direct `os.Remove` calls on synced content anywhere else in the codebase (lint/grep check in CI is acceptable enforcement).

### Phase 6 — local watcher & remote poller
- `internal/watcher`: `fsnotify` recursive watch per registered local folder pair, debounce window before triggering a sync-engine check.
- `internal/poller`: adaptive interval (baseline 60s / active 15s / idle 5-10m per config), shared "activity" signal with the watcher so either side tightens the other's cadence; ties into `pacer` so a sustained rate-limit signal independently extends the interval.
- **Done when**: editing a file in a watched local folder triggers a sync-engine decision within the debounce window (integration test against the fake API server); simulated remote-only change is picked up within one poll interval.
- note: local fsnotify watches are set up only for registered pairs' local roots — if a pair is removed, its watch must be torn down (resource leak check).

### Phase 7 — CLI + daemon wiring, IPC
- `cmd/isyncd`: wires watcher + poller + syncengine + credstore + stateindex + apiclient(real) into a running daemon process; reads master password via systemd `LoadCredential=`/env at startup (no TTY assumed).
- `cmd/isync`: `login`, `add`, `remove`, `list`, `status`, `sync [pair-id]`, `daemon` (foreground entrypoint used by the systemd unit's `ExecStart`).
- `internal/ipc`: CLI reads daemon-written status (simplest: daemon periodically writes a status snapshot file/small unix-socket responder; avoid over-engineering — a status file polled by `isync status` is sufficient for v1).
- **Done when**: full local loop from spec.md's "definition of done" works against the fake server in an integration test: `isync login` (fake 2FA) → `isync add` → start daemon → simulate edit on either side → observe convergence via `isync status` → restart daemon → confirm no re-auth needed (token persisted) → simulate token expiry → confirm clear re-auth prompt, not a silent retry loop.

### Phase 8 — packaging & docs
- `systemd/isyncd.service` template (user unit, `LoadCredential=` example, restart policy).
- `scripts/install.sh` / `make install`: build, place binary, install unit file, create XDG dirs.
- README: setup instructions, 2FA login walkthrough, folder-pair management, the reverse-engineered-API disclaimer (per INTENT.md Constraints — must be clearly disclosed), troubleshooting (re-auth, oversized-file skip, rate-limit backoff visibility via `isync status`).
- **Done when**: a clean Linux VM can go from `git clone` to a running daemon syncing a real folder pair using only the README.

### Phase 9 — real-world validation pass
- Manual smoke test against a real throwaway Apple ID covering: full login+2FA, two-way sync of a small folder (create/edit/delete on both sides), a conflict scenario (edit both sides between polls), one file above the size threshold (confirm skip+flag, not a hang), observe pacer behavior under intentionally rapid repeated syncs.
- **Done when**: checklist in spec.md §2.5 passes end-to-end; file any discovered real-API deviations as follow-up intents (see §4 below) rather than silently patching scope mid-plan.

## 3. Risks & mitigations

| Risk | Mitigation |
|---|---|
| Apple changes the private API, breaking auth/list/upload without notice | `apiclient` isolated behind interface (Phase 2); fake server + real client are swappable independently; README discloses this risk to users up front. |
| Account gets rate-limited/flagged during development | Always default to the fake server for day-to-day dev/test; real-API phases (3, 9) use a throwaway account, not the author's primary Apple ID, and run manually/infrequently, never in CI. |
| 2FA flow is awkward to test/automate | Fake server implements a scriptable 2FA challenge/response so integration tests cover the flow without real interactivity; manual phases are the only place real interactive 2FA is exercised. |
| Data loss from a sync-logic bug (2-way sync is inherently riskier than one-way) | Trash/quarantine (Phase 5) is mandatory on every delete path, enforced structurally (no direct unlink calls), and combined with iCloud's own Recently Deleted as a second line of defense (already decided in INTENT.md). |
| Clock skew between local machine and Apple's servers causing false conflicts | Hash-fallback diff logic (Phase 4) avoids relying on mtime comparison alone. |
| Daemon can't get the master password without a TTY | systemd `LoadCredential=`/env path designed in from Phase 1 onward, not bolted on later. |
| Scope creep beyond v1 (Photos, multi-account, GUI) | Explicit non-goals from spec.md §1.3 — any such request becomes a new `intent.md`, not a mid-plan addition. |

## 4. Success criteria (overall v1 "done")

Matches INTENT.md's definition of done, operationalized:

1. `isync login` completes real Apple ID + 2FA auth and persists an encrypted credential file.
2. `isync add <icloud-path> <local-path>` registers a folder pair.
3. Starting the daemon (`systemctl --user start isyncd`) begins syncing that pair.
4. A file edited locally appears in iCloud Drive (verifiable via icloud.com or another Apple device) within one sync interval, and vice versa.
5. A conflicting edit (both sides changed) resolves via last-write-wins, and the losing version is recoverable from the local trash.
6. A deleted file is recoverable from the local trash within the retention window.
7. An oversized file (>10GiB) is skipped and visibly flagged, not retried forever.
8. Restarting the daemon and letting a token expire both recover without manual file-system intervention — re-auth is prompted, not silently broken.
9. All of the above (except the real-API login itself) is covered by automated tests running against the fake API server in CI.

## 5. Explicitly deferred (do not implement in v1)

Per INTENT.md/spec.md non-goals: iCloud Photos/Mail/Contacts/Find My/Hide My
Email, multi-account support, multi-user/shared-machine support, Windows/
macOS builds, any GUI, distro-native packages (.deb/.rpm/AUR/Nix), and
client-side file chunking for oversized files. If any of these become
desired, they start as a new `intent.md`, not a plan-stage addition.
