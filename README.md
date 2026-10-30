# iSync

iSync is an Apache-2.0-licensed iCloud Drive sync client for Linux. It performs
reliable, two-way syncing between selected iCloud Drive folders and local
folders, runs as a systemd user service, and keeps your credentials encrypted
on disk.

> **⚠️ Reverse-engineered API disclaimer:** iSync talks to Apple's private,
> undocumented iCloud web API. Apple can change or break this API without
> notice. The iCloud API client is isolated behind a narrow internal interface
> (`internal/apiclient`) so it can be patched independently of the sync engine,
> but real-API behavior can only be validated manually. Use a throwaway Apple ID
> for the first real sync and keep backups of anything you cannot afford to
> lose.

## What it does

- `isync login` — authenticate with an Apple ID (interactive 2FA supported).
- `isync add <icloud-path> <local-path>` — register a folder pair.
- `isync list` / `isync remove <pair-id>` — manage pairs.
- `isync status` — read the daemon's live status snapshot.
- `isync sync [pair-id]` — run a one-off sync from the CLI.
- `isyncd` — background daemon (systemd `ExecStart` target) that watches local
  folders and polls iCloud adaptively.

Sync decisions:

- New/changed files are uploaded or downloaded.
- Deletes are propagated after confirming the missing side was previously synced;
  the deleted copy is moved to a local trash/quarantine directory, not unlinked.
- Conflicts (both sides changed) resolve via last-write-wins by modification
  timestamp; the losing version ends up in trash.
- Files above the configured max size (default 10 GiB) are skipped and flagged.

## Quick start

### 1. Install

From a clone of this repository on a Linux amd64 or arm64 machine with Go
1.23+:

```bash
make install
```

This builds static `isync` and `isyncd` binaries into `~/.local/bin`, installs
`systemd/isyncd.service` as a user unit, and creates the iSync config/state
 directories.

### 2. Set the master passphrase

The daemon needs your iSync master passphrase at startup. With systemd:

```bash
mkdir -p ~/.config/isync
install -m 0600 /dev/null ~/.config/isync/master-passphrase
# Edit the file and write your passphrase on a single line.
systemctl --user daemon-reload
systemctl --user enable --now isyncd
```

Without systemd, set the environment variable before running the daemon:

```bash
export ISYNC_MASTER_PASSPHRASE="your-passphrase"
isyncd
```

### 3. Log in

```bash
isync login
```

You will be prompted for:

- Apple ID
- Apple ID password
- 2FA code (if required)
- iSync master passphrase

Your credentials are encrypted with `age`+scrypt and stored at
`~/.config/isync/credentials.age`. The session token is stored inside the same
encrypted file so the daemon can resume after a restart.

### 4. Add a folder pair

```bash
mkdir -p ~/iCloudDocuments
isync add documents ~/iCloudDocuments
# Enter the iCloud Drive path when prompted, e.g. /Documents
```

### 5. Start the daemon

```bash
systemctl --user start isyncd
isync status
```

After a short delay (baseline poll 60 s, or faster after local/remote
activity), files begin syncing in both directions.

## Configuration

TOML file at `~/.config/isync/config.toml`:

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
enabled = true
```

Settings not present in the file use built-in defaults.

## File locations

| Purpose | Default path |
|---|---|
| Config | `~/.config/isync/config.toml` |
| Encrypted credentials | `~/.config/isync/credentials.age` |
| State database (BoltDB) | `~/.local/share/isync/state.db` |
| Trash/quarantine | `~/.local/share/isync/trash/<YYYY-MM-DDTHH-MM>/...` |
| Daemon status file | `$XDG_RUNTIME_DIR/isync/status.json` or `~/.local/share/isync/status.json` |

All directories are created with mode `0700`; credential files with `0600`.

## Troubleshooting

### Daemon status

```bash
isync status
```

Shows health, per-pair last-sync time, errors, and recent pacer state.

### Re-authentication required

If the session or trust token expires, `isync status` will show an auth error
and sync stops. Run:

```bash
isync login
systemctl --user restart isyncd
```

The daemon never silently retries auth failures forever.

### Oversized files

Files larger than `max_file_size` are skipped. They remain flagged in the
daemon status until you move/remove them or raise the limit.

### Rate limiting

If iCloud throttles requests, the adaptive pacer increases inter-request delay
(up to a cap) and the status output reflects the current backoff. Avoid
manually hammering `isync sync` in a tight loop.

### Data recovery

Deleted synced files land in `~/.local/share/isync/trash/` with a timestamped
bucket. Trash older than `trash_retention_days` is cleaned up automatically by
the daemon. iCloud's own "Recently Deleted" is an additional safety layer for
remote deletes.

## Security notes

- Credentials and session tokens are encrypted with `age` using a scrypt-derived
  key from your master passphrase. No passwords or tokens are written to logs.
- The daemon obtains the passphrase via systemd `LoadCredential=` or the
  `ISYNC_MASTER_PASSPHRASE` environment variable; it does not need a TTY.
- No telemetry is collected. The only network traffic is to Apple's own API
  endpoints required to perform sync.

## Building and testing

```bash
make build    # builds bin/isync and bin/isyncd
make test     # go test ./... -race -cover
make vet      # go vet ./...
make install  # install to ~/.local and register systemd unit
```

All automated tests run against an in-memory fake iCloud API server. Manual
real-API smoke testing with a throwaway Apple ID is required before any
release.

## Architecture

- `cmd/isync` — CLI entrypoint.
- `cmd/isyncd` — daemon entrypoint.
- `internal/apiclient` — shared client interface; `real` talks to Apple, `fake`
  is used in tests.
- `internal/syncengine` — diff/conflict/transfer decision logic.
- `internal/stateindex` — BoltDB-backed per-pair state.
- `internal/watcher` — `fsnotify` local watcher with debounce.
- `internal/poller` — adaptive remote polling.
- `internal/pacer` — rate-limit pacer + concurrency limiter.
- `internal/trash` — quarantine-on-delete + retention cleanup.
- `internal/credstore` — `age`-encrypted credential file.
- `internal/ipc` — atomic status snapshot file.
- `systemd/isyncd.service` — user unit template.
- `scripts/install.sh` — install script.

## License

Apache License 2.0. See `LICENSE` and `NOTICE`.

## Non-goals for v1

- iCloud Photos, Mail, Contacts, Find My, Hide My Email.
- Multi-account or multi-user support.
- Windows/macOS builds or any GUI.
- Distro-native packages (.deb/.rpm/AUR/Nix).
- Syncing the entire iCloud Drive (opt-in per folder pair).
- Client-side file chunking.
