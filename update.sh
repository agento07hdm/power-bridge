#!/usr/bin/env bash
# =============================================================================
# power-bridge update.sh
# Update check + install via GitHub Releases.
#
# Behaviour:
#   - Checks GitHub API for a newer release (stable channel by default)
#   - In "check" mode (default, used at boot): only checks and logs whether an
#     update is available. Does NOT download or install anything, unless
#     /etc/power-bridge/auto-update contains "true" (opt-in).
#   - In "apply" mode (triggered by the web UI's "Jetzt aktualisieren" button,
#     or by running this script manually with the "apply" argument): downloads
#     the release, verifies its SHA256 checksum against the published
#     SHA256SUMS file, verifies GitHub build provenance (attestation) for the
#     binary, and only then installs it.
#   - Creates a rollback backup before replacing the binary
#   - Skips silently when there is no internet connectivity
#   - Logs all actions to the systemd journal (via logger) and stdout
#
# Channel selection (optional):
#   echo "beta" | sudo tee /etc/power-bridge/update-channel
#
# Auto-install at boot (optional, off by default):
#   echo "true" | sudo tee /etc/power-bridge/auto-update
#
# Usage:
#   sudo bash /usr/local/share/power-bridge/update.sh check   # check only (default)
#   sudo bash /usr/local/share/power-bridge/update.sh apply   # check, verify, install
#
# At boot this script is called with "check" by power-bridge-update.service
# (oneshot), which must complete before power-bridge.service starts.
# =============================================================================
set -euo pipefail

MODE="${1:-check}"

REPO="agento07hdm/power-bridge"
BINARY_DEST="/usr/local/bin/power-bridge"
SHARE_DIR="/usr/local/share/power-bridge"
VERSION_FILE="/etc/power-bridge/VERSION"
CHANNEL_FILE="/etc/power-bridge/update-channel"
AUTO_UPDATE_FILE="/etc/power-bridge/auto-update"
BACKUP_BINARY="${SHARE_DIR}/power-bridge.bak"
BACKUP_VERSION="${SHARE_DIR}/VERSION.bak"
LOG_TAG="power-bridge-update"

# gh CLI is used only to verify GitHub build-provenance attestations. Pinned
# version + hardcoded checksum so a compromised download can't smuggle in a
# tampered verifier.
GH_CLI_VERSION="2.98.0"
GH_CLI_SHA256="2c1706b6ff1f10bf93a0b370bc61e45f5e1fd78379361f414c5ac05bc5bf75d3"
GH_CLI_DIR="${SHARE_DIR}/gh-cli"
GH_CLI_BIN="${GH_CLI_DIR}/gh"

# ── Logging helpers ───────────────────────────────────────────────────────────
log()  { echo "[$(date '+%Y-%m-%d %H:%M:%S')]  $*"; logger -t "$LOG_TAG" "$*" 2>/dev/null || true; }
ok()   { log "✓ $*"; }
warn() { log "⚠ $*"; }
err()  { log "✗ $*" >&2; }

# ── Root check ────────────────────────────────────────────────────────────────
[ "$(id -u)" -eq 0 ] || { err "Please run as root"; exit 1; }

# ── Fetch (or reuse a cached) gh CLI, used to verify build provenance ────────
ensure_gh_cli() {
    if command -v gh >/dev/null 2>&1; then
        GH_CLI_BIN=$(command -v gh)
        return 0
    fi
    [ -x "$GH_CLI_BIN" ] && return 0

    log "Fetching gh CLI (one-time download, used to verify build provenance)…"
    local tarball
    tarball=$(mktemp /tmp/gh-cli-XXXXXX.tar.gz)
    if ! curl -fsSL --max-time 60 \
        "https://github.com/cli/cli/releases/download/v${GH_CLI_VERSION}/gh_${GH_CLI_VERSION}_linux_armv6.tar.gz" \
        -o "$tarball" 2>/dev/null; then
        warn "Could not download gh CLI – provenance verification unavailable"
        rm -f "$tarball"
        return 1
    fi

    local actual_sha
    actual_sha=$(sha256sum "$tarball" | awk '{print $1}')
    if [ "$actual_sha" != "$GH_CLI_SHA256" ]; then
        err "gh CLI download checksum mismatch – refusing to use it"
        rm -f "$tarball"
        return 1
    fi

    mkdir -p "$GH_CLI_DIR"
    if ! tar -xzf "$tarball" -O "gh_${GH_CLI_VERSION}_linux_armv6/bin/gh" > "${GH_CLI_BIN}.tmp" 2>/dev/null; then
        warn "Could not extract gh CLI – provenance verification unavailable"
        rm -f "$tarball" "${GH_CLI_BIN}.tmp"
        return 1
    fi
    mv -f "${GH_CLI_BIN}.tmp" "$GH_CLI_BIN"
    chmod 755 "$GH_CLI_BIN"
    rm -f "$tarball"
    [ -x "$GH_CLI_BIN" ]
}

# ── Verify GitHub build provenance (attestation) for a downloaded file ───────
# Ties the binary back to the exact commit + workflow run that built it, so a
# release asset re-uploaded via a leaked token (without a matching CI run)
# fails verification even if it happens to carry a matching checksum.
verify_attestation() {
    local file="$1"
    if ! ensure_gh_cli; then
        return 2
    fi
    if "$GH_CLI_BIN" attestation verify "$file" \
        --repo "$REPO" \
        --signer-workflow "${REPO}/.github/workflows/release.yml" \
        > /tmp/power-bridge-attestation.log 2>&1; then
        return 0
    fi
    while IFS= read -r line; do err "  $line"; done < /tmp/power-bridge-attestation.log
    return 1
}

# ── Read update channel (stable / beta), default: stable ─────────────────────
CHANNEL="stable"
if [ -f "$CHANNEL_FILE" ]; then
    CHANNEL=$(tr -d '[:space:]' < "$CHANNEL_FILE")
fi

# ── Read auto-update opt-in (default: false = manual install only) ──────────
AUTO_UPDATE="false"
if [ -f "$AUTO_UPDATE_FILE" ]; then
    AUTO_UPDATE=$(tr -d '[:space:]' < "$AUTO_UPDATE_FILE")
fi

# ── Read currently installed version ─────────────────────────────────────────
INSTALLED_VERSION="unknown"
if [ -f "$VERSION_FILE" ]; then
    INSTALLED_VERSION=$(tr -d '[:space:]' < "$VERSION_FILE")
fi

log "power-bridge update check started | installed=$INSTALLED_VERSION channel=$CHANNEL mode=$MODE auto_update=$AUTO_UPDATE"

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

# ── Manual-by-default gate: only proceed past this point if the user (or an
#    explicit opt-in) actually asked for the update to be installed now ──────
if [ "$MODE" != "apply" ] && [ "$AUTO_UPDATE" != "true" ]; then
    log "Update available but not installed automatically. Open the power-bridge web UI and click 'Jetzt aktualisieren', or run: sudo bash $0 apply"
    exit 0
fi

# ── Download new binary to a temp file ───────────────────────────────────────
BINARY_NAME="power-bridge-${LATEST_VERSION}-linux-armv6"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${LATEST_VERSION}/${BINARY_NAME}"
TEMP_BINARY=$(mktemp /tmp/power-bridge-update-XXXXXX)

# Always clean up temp files on exit (success or failure)
trap 'rm -f "$TEMP_BINARY" "$TEMP_SUMS" "${BINARY_DEST}.tmp" 2>/dev/null || true' EXIT

log "Downloading $DOWNLOAD_URL …"
if ! curl -fsSL --max-time 120 "$DOWNLOAD_URL" -o "$TEMP_BINARY"; then
    err "Download failed: $DOWNLOAD_URL – keeping current version $INSTALLED_VERSION"
    exit 0
fi

# Sanity: ensure the downloaded file is non-empty
if [ ! -s "$TEMP_BINARY" ]; then
    err "Downloaded file is empty – aborting update"
    exit 0
fi

ok "Binary downloaded ($(du -sh "$TEMP_BINARY" | cut -f1))"

# ── Verify SHA256 checksum against the release's published SHA256SUMS ───────
SUMS_URL="https://github.com/${REPO}/releases/download/${LATEST_VERSION}/SHA256SUMS"
TEMP_SUMS=$(mktemp /tmp/power-bridge-sums-XXXXXX)
if ! curl -fsSL --max-time 30 "$SUMS_URL" -o "$TEMP_SUMS"; then
    err "Could not download SHA256SUMS – refusing to install an unverified binary"
    exit 0
fi
EXPECTED_SHA=$(grep " ${BINARY_NAME}\$" "$TEMP_SUMS" 2>/dev/null | awk '{print $1}')
if [ -z "$EXPECTED_SHA" ]; then
    err "No checksum entry for $BINARY_NAME in SHA256SUMS – aborting update"
    exit 0
fi
ACTUAL_SHA=$(sha256sum "$TEMP_BINARY" | awk '{print $1}')
if [ "$ACTUAL_SHA" != "$EXPECTED_SHA" ]; then
    err "Checksum mismatch for $BINARY_NAME (expected $EXPECTED_SHA, got $ACTUAL_SHA) – aborting update"
    exit 0
fi
ok "Checksum verified"

# ── Verify GitHub build provenance (attestation) ─────────────────────────────
verify_attestation "$TEMP_BINARY"
ATTESTATION_STATUS=$?
if [ "$ATTESTATION_STATUS" -eq 1 ]; then
    err "Build provenance verification failed – aborting update"
    exit 0
elif [ "$ATTESTATION_STATUS" -eq 2 ]; then
    warn "Build provenance NOT verified (gh CLI unavailable) – proceeding on checksum alone"
else
    ok "Build provenance verified (signed by GitHub Actions for $REPO)"
fi

chmod 755 "$TEMP_BINARY"

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
