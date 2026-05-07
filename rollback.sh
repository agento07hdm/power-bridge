#!/usr/bin/env bash
# =============================================================================
# power-bridge rollback.sh
# Restore the previous binary and version after a failed update.
#
# Usage:
#   sudo bash /usr/local/share/power-bridge/rollback.sh
#
# The rollback restores the binary saved by update.sh before the last update.
# =============================================================================
set -euo pipefail

BINARY_DEST="/usr/local/bin/power-bridge"
SHARE_DIR="/usr/local/share/power-bridge"
VERSION_FILE="/etc/power-bridge/VERSION"
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

log "power-bridge rollback initiated"

# ── Check backup availability ─────────────────────────────────────────────────
if [ ! -f "$BACKUP_BINARY" ]; then
    err "No backup binary found at $BACKUP_BINARY – cannot rollback"
    exit 1
fi

CURRENT_VERSION="unknown"
[ -f "$VERSION_FILE" ] && CURRENT_VERSION=$(tr -d '[:space:]' < "$VERSION_FILE")

BACKUP_VER="unknown"
[ -f "$BACKUP_VERSION" ] && BACKUP_VER=$(tr -d '[:space:]' < "$BACKUP_VERSION")

log "Rolling back: $CURRENT_VERSION → $BACKUP_VER"

# ── Stop power-bridge service ─────────────────────────────────────────────────
log "Stopping power-bridge service…"
systemctl stop power-bridge.service 2>/dev/null || true
sleep 1

# ── Restore backup binary (atomic rename) ────────────────────────────────────
cp -f "$BACKUP_BINARY" "${BINARY_DEST}.tmp"
mv -f "${BINARY_DEST}.tmp" "$BINARY_DEST"
chmod 755 "$BINARY_DEST"
ok "Binary restored from backup"

# ── Restore version file ──────────────────────────────────────────────────────
if [ -f "$BACKUP_VERSION" ]; then
    cp -f "$BACKUP_VERSION" "$VERSION_FILE"
    ok "VERSION restored to $BACKUP_VER"
fi

# ── Remove backup (only keep one level of rollback) ──────────────────────────
rm -f "$BACKUP_BINARY" "$BACKUP_VERSION"
ok "Backup files removed"

# ── Restart power-bridge service ─────────────────────────────────────────────
log "Starting power-bridge service…"
systemctl start power-bridge.service 2>/dev/null || {
    err "Failed to start power-bridge.service after rollback"
    exit 1
}

ok "Rollback complete – now running $BACKUP_VER"
log "power-bridge rollback finished | version=$BACKUP_VER"
exit 0
