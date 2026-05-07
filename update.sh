#!/usr/bin/env bash
# =============================================================================
# power-bridge update.sh
# Boot-time OTA update via GitHub Releases.
#
# Behaviour:
#   - Checks GitHub API for a newer release (stable channel by default)
#   - Downloads and atomically installs the new binary if a newer version exists
#   - Creates a rollback backup before replacing the binary
#   - Skips silently when there is no internet connectivity
#   - Logs all actions to the systemd journal (via logger) and stdout
#
# Channel selection (optional):
#   echo "beta" | sudo tee /etc/power-bridge/update-channel
#
# Usage (standalone / manual):
#   sudo bash /usr/local/share/power-bridge/update.sh
#
# At boot this script is called by power-bridge-update.service (oneshot),
# which must complete before power-bridge.service starts.
# =============================================================================
set -euo pipefail

REPO="fedzzito/power-bridge"
BINARY_DEST="/usr/local/bin/power-bridge"
SHARE_DIR="/usr/local/share/power-bridge"
VERSION_FILE="/etc/power-bridge/VERSION"
CHANNEL_FILE="/etc/power-bridge/update-channel"
BACKUP_BINARY="${SHARE_DIR}/power-bridge.bak"
BACKUP_VERSION="${SHARE_DIR}/VERSION.bak"
LOG_TAG="power-bridge-update"

# ── Logging helpers ───────────────────────────────────────────────────────────
log()  { echo "[$(date '+%Y-%m-%d %H:%M:%S')]  $*"; logger -t "$LOG_TAG" "$*" 2>/dev/null || true; }
ok()   { log "✓ $*"; }
warn() { log "⚠ $*"; }
err()  { log "✗ $*" >&2; }

# ── Root check ────────────────────────────────────────────────────────────────
[ "$(id -u)" -eq 0 ] || { err "Please run as root"; exit 1; }

# ── Read update channel (stable / beta), default: stable ─────────────────────
CHANNEL="stable"
if [ -f "$CHANNEL_FILE" ]; then
    CHANNEL=$(tr -d '[:space:]' < "$CHANNEL_FILE")
fi

# ── Read currently installed version ─────────────────────────────────────────
INSTALLED_VERSION="unknown"
if [ -f "$VERSION_FILE" ]; then
    INSTALLED_VERSION=$(tr -d '[:space:]' < "$VERSION_FILE")
fi

log "power-bridge update check started | installed=$INSTALLED_VERSION channel=$CHANNEL"

# ── Check internet connectivity (fast, non-blocking) ─────────────────────────
if ! curl -fsSL --max-time 5 --head "https://api.github.com" > /dev/null 2>&1; then
    warn "No internet connection – skipping update, continuing with $INSTALLED_VERSION"
    exit 0
fi

# ── Fetch latest release metadata from GitHub API ────────────────────────────
LATEST_VERSION=""
CHANGELOG=""

if [ "$CHANNEL" = "stable" ]; then
    RELEASE_JSON=$(curl -fsSL --max-time 15 \
        "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null) || {
        warn "GitHub API unreachable – skipping update"
        exit 0
    }
    LATEST_VERSION=$(echo "$RELEASE_JSON" \
        | grep '"tag_name"' | head -1 \
        | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
    # Extract first 5 lines of release notes for the log
    CHANGELOG=$(echo "$RELEASE_JSON" \
        | grep '"body"' | head -1 \
        | sed 's/.*"body": *"\(.*\)".*/\1/' \
        | sed 's/\\n/\n/g; s/\\r//g' \
        | head -5) || CHANGELOG=""
else
    # Beta: use the first pre-release available; fall back to latest stable
    ALL_JSON=$(curl -fsSL --max-time 15 \
        "https://api.github.com/repos/${REPO}/releases" 2>/dev/null) || {
        warn "GitHub API unreachable – skipping update"
        exit 0
    }
    # Look for first pre-release tag_name
    LATEST_VERSION=$(echo "$ALL_JSON" \
        | awk '/"prerelease": true/{found=1} found && /"tag_name"/{
            gsub(/.*"tag_name": *"/, ""); gsub(/".*/, ""); print; exit}')
    # Fall back to first stable release if no pre-release found
    if [ -z "$LATEST_VERSION" ]; then
        LATEST_VERSION=$(echo "$ALL_JSON" \
            | grep '"tag_name"' | head -1 \
            | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
    fi
fi

if [ -z "$LATEST_VERSION" ]; then
    warn "Could not determine latest version from GitHub API – skipping update"
    exit 0
fi

log "Latest available version: $LATEST_VERSION"
[ -n "$CHANGELOG" ] && log "Release notes: $CHANGELOG"

# ── Compare versions: skip if already up to date ─────────────────────────────
if [ "$INSTALLED_VERSION" = "$LATEST_VERSION" ]; then
    ok "Already up to date ($INSTALLED_VERSION) – no update needed"
    exit 0
fi

log "Update available: $INSTALLED_VERSION → $LATEST_VERSION"

# ── Download new binary to a temp file ───────────────────────────────────────
BINARY_NAME="power-bridge-${LATEST_VERSION}-linux-armv6"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${LATEST_VERSION}/${BINARY_NAME}"
TEMP_BINARY=$(mktemp /tmp/power-bridge-update-XXXXXX)

# Always clean up the temp file on exit (success or failure)
trap 'rm -f "$TEMP_BINARY" "${BINARY_DEST}.tmp" 2>/dev/null || true' EXIT

log "Downloading $DOWNLOAD_URL …"
if ! curl -fsSL --max-time 120 "$DOWNLOAD_URL" -o "$TEMP_BINARY"; then
    err "Download failed: $DOWNLOAD_URL – keeping current version $INSTALLED_VERSION"
    exit 0
fi

# Sanity: ensure the downloaded file is non-empty and executable-like
if [ ! -s "$TEMP_BINARY" ]; then
    err "Downloaded file is empty – aborting update"
    exit 0
fi

chmod 755 "$TEMP_BINARY"
ok "Binary downloaded successfully ($(du -sh "$TEMP_BINARY" | cut -f1))"

# ── Backup current binary and version before replacing ───────────────────────
mkdir -p "$SHARE_DIR"
if [ -f "$BINARY_DEST" ]; then
    cp -f "$BINARY_DEST" "$BACKUP_BINARY"
    ok "Backup saved: $BACKUP_BINARY"
fi
if [ -f "$VERSION_FILE" ]; then
    cp -f "$VERSION_FILE" "$BACKUP_VERSION"
fi

# ── Atomic binary install (rename is atomic on the same filesystem) ───────────
# Copy to a .tmp file first, then rename – robust against power failure mid-write.
cp -f "$TEMP_BINARY" "${BINARY_DEST}.tmp"
mv -f "${BINARY_DEST}.tmp" "$BINARY_DEST"
chmod 755 "$BINARY_DEST"

# ── Persist new version ───────────────────────────────────────────────────────
mkdir -p "$(dirname "$VERSION_FILE")"
echo "$LATEST_VERSION" > "$VERSION_FILE"

ok "Update complete: $INSTALLED_VERSION → $LATEST_VERSION"
log "power-bridge update finished successfully | version=$LATEST_VERSION"
exit 0
