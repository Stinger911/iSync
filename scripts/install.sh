#!/bin/bash
set -euo pipefail

PREFIX="${PREFIX:-${HOME}/.local}"
BINDIR="${PREFIX}/bin"
SYSTEMD_USER_DIR="${SYSTEMD_USER_DIR:-${HOME}/.config/systemd/user}"

usage() {
    echo "Usage: $0 [--prefix PREFIX] [--systemd-dir DIR]"
    echo "  default prefix: ~/.local"
    echo "  default systemd user dir: ~/.config/systemd/user"
    exit 1
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --prefix)
            PREFIX="$2"
            BINDIR="${PREFIX}/bin"
            shift 2
            ;;
        --systemd-dir)
            SYSTEMD_USER_DIR="$2"
            shift 2
            ;;
        -h|--help)
            usage
            ;;
        *)
            echo "Unknown option: $1" >&2
            usage
            ;;
    esac
done

# Build static binaries for the host architecture.
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "${BINDIR}/isync" ./cmd/isync
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "${BINDIR}/isyncd" ./cmd/isyncd

# Install the systemd user unit.
mkdir -p "${SYSTEMD_USER_DIR}"
cp systemd/isyncd.service "${SYSTEMD_USER_DIR}/isyncd.service"

# Create runtime/state directories.
mkdir -p "${HOME}/.config/isync"
mkdir -p "${HOME}/.local/share/isync"
mkdir -p "${HOME}/.local/state/isync"
mkdir -p "${XDG_RUNTIME_DIR:-/run/user/$(id - u)}/isync" 2>/dev/null || true

# Install a placeholder credential file with strict permissions.
if [[ ! -f "${HOME}/.config/isync/master-passphrase" ]]; then
    install -m 0600 /dev/null "${HOME}/.config/isync/master-passphrase"
    cat >&2 <<'EOF'
Created ~/.config/isync/master-passphrase (empty).
Write your iSync master passphrase there and run:
  systemctl --user daemon-reload
  systemctl --user enable --now isyncd
EOF
else
    echo "~/.config/isync/master-passphrase already exists; not overwriting." >&2
fi

# Reload systemd if running under systemd.
if systemctl --user daemon-reload 2>/dev/null; then
    echo "Run 'systemctl --user enable --now isyncd' to start the daemon." >&2
else
    echo "systemd not available or not running; unit file installed at ${SYSTEMD_USER_DIR}." >&2
fi
