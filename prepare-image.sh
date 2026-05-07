#!/usr/bin/env bash
# =============================================================================
# power-bridge prepare-image.sh
# Run on the Pi BEFORE creating a SD-card image for redistribution.
#
# Prepares the system for cloning by:
#   - Stopping all power-bridge services
#   - Resetting configuration to unconfigured defaults
#   - Removing SSH host keys (regenerated on first boot)
#   - Clearing the machine-ID
#   - Cleaning package caches (apt, pip, npm)
#   - Shrinking / truncating log files
#   - Removing DHCP leases and temp files
#   - Filling free disk space with zeros (better compression for .img files)
#   - Ensuring first-boot filesystem resize is enabled
#
# Recommended workflow:
#   1. sudo bash /usr/local/share/power-bridge/prepare-image.sh
#   2. sudo shutdown -h now
#   3. Create image with USBImager (Windows/macOS/Linux)
#   4. Optional: shrink with PiShrink
#        pishrink.sh -za power-bridge.img
#
# USBImager:   https://bztsrc.gitlab.io/usbimager/
# PiShrink:    https://github.com/Drewsif/PiShrink
#
# Usage: sudo bash /usr/local/share/power-bridge/prepare-image.sh
# =============================================================================
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
ok()   { echo -e "${GREEN}✓${NC} $*"; }
warn() { echo -e "${YELLOW}⚠${NC}  $*"; }
err()  { echo -e "${RED}✗${NC} $*" >&2; exit 1; }
step() { echo -e "\n${GREEN}[→]${NC} $*"; }

[ "$(id -u)" -eq 0 ] || err "Please run as root: sudo bash prepare-image.sh"

echo ""
echo "=================================================================="
echo "  power-bridge – prepare image for redistribution"
echo "=================================================================="
echo ""

# ── 1. Stop services ──────────────────────────────────────────────────────────
step "Stopping power-bridge services…"
systemctl stop power-bridge.service 2>/dev/null || true
systemctl stop power-bridge-update.service 2>/dev/null || true
ok "Services stopped"

# ── 2. Reset config to unconfigured defaults ──────────────────────────────────
step "Resetting configuration…"
if [ -f /etc/power-bridge/config.yaml ]; then
    cat > /etc/power-bridge/config.yaml << 'EOF'
# power-bridge configuration
# Edit this file or use the web setup UI at http://192.168.4.1 (AP mode)

wifi_ssid: ""
wifi_password: ""
poweropti_ip: ""
poweropti_api_key: ""
shelly_mac: "AA:BB:CC:DD:EE:FF"
hostname: "shellypro3em-poweropti"
device_profile: "standard"
phase_mode: "equal"
poll_interval_sec: 3
stale_timeout_sec: 30
listen_addr: ":80"
configured: false
EOF
    ok "Config reset to defaults (configured: false)"
fi

# Reset update channel to stable
echo "stable" > /etc/power-bridge/update-channel 2>/dev/null || true

# ── 3. Remove SSH host keys (regenerated on first boot) ───────────────────────
step "Removing SSH host keys…"
rm -f /etc/ssh/ssh_host_*
# Raspberry Pi OS Bookworm/Bullseye: enable first-boot key regeneration
if [ -f /lib/systemd/system/regenerate_ssh_host_keys.service ]; then
    systemctl enable regenerate_ssh_host_keys 2>/dev/null || true
    ok "SSH host key regeneration enabled for first boot"
fi
ok "SSH host keys removed"

# ── 4. Clear machine-ID (new ID generated on first boot) ──────────────────────
step "Clearing machine-ID…"
truncate -s 0 /etc/machine-id
rm -f /var/lib/dbus/machine-id
ok "Machine-ID cleared"

# ── 5. Clean apt package cache ────────────────────────────────────────────────
step "Cleaning apt cache…"
apt-get clean -y 2>/dev/null || true
apt-get autoremove -y --purge 2>/dev/null || true
rm -rf /var/lib/apt/lists/*
ok "apt cache cleaned"

# ── 6. Clean pip cache ────────────────────────────────────────────────────────
step "Cleaning pip cache…"
pip3 cache purge 2>/dev/null || true
pip cache purge 2>/dev/null || true
rm -rf /root/.cache/pip 2>/dev/null || true
rm -rf /home/pi/.cache/pip 2>/dev/null || true
ok "pip cache cleaned (if present)"

# ── 7. Clean npm cache ────────────────────────────────────────────────────────
step "Cleaning npm cache…"
if command -v npm > /dev/null 2>&1; then
    npm cache clean --force 2>/dev/null || true
fi
rm -rf /root/.npm 2>/dev/null || true
rm -rf /home/pi/.npm 2>/dev/null || true
ok "npm cache cleaned (if present)"

# ── 8. Remove temporary files and build artefacts ────────────────────────────
step "Removing temporary files…"
rm -rf /tmp/* /var/tmp/* 2>/dev/null || true
rm -rf /root/.cache/go-build 2>/dev/null || true
rm -rf /home/pi/.cache/go-build 2>/dev/null || true
ok "Temporary files removed"

# ── 9. Truncate / remove log files ───────────────────────────────────────────
step "Cleaning log files…"
journalctl --vacuum-time=1s 2>/dev/null || true
find /var/log -type f \( -name "*.log" -o -name "syslog" \
    -o -name "messages" -o -name "auth.log" -o -name "kern.log" \) \
    -exec truncate -s 0 {} \; 2>/dev/null || true
find /var/log -type f -name "*.gz" -delete 2>/dev/null || true
find /var/log -type f -name "*.[0-9]" -delete 2>/dev/null || true
ok "Log files cleaned"

# ── 10. Remove DHCP leases ────────────────────────────────────────────────────
step "Removing DHCP leases…"
rm -f /var/lib/dhcp/*.leases 2>/dev/null || true
rm -f /var/lib/dhcpcd5/*.lease 2>/dev/null || true
rm -f /var/lib/dhcpcd/*.lease 2>/dev/null || true
ok "DHCP leases removed"

# ── 11. Clear bash / shell history ───────────────────────────────────────────
step "Clearing shell history…"
rm -f /root/.bash_history /home/pi/.bash_history 2>/dev/null || true
history -c 2>/dev/null || true
ok "Shell history cleared"

# ── 12. Ensure first-boot filesystem resize is enabled ───────────────────────
step "Checking first-boot resize configuration…"
CMDLINE_FILE=""
for f in /boot/firmware/cmdline.txt /boot/cmdline.txt; do
    [ -f "$f" ] && CMDLINE_FILE="$f" && break
done

if [ -n "$CMDLINE_FILE" ]; then
    if ! grep -q "init=/usr/lib/raspi-config/init_resize.sh" "$CMDLINE_FILE" 2>/dev/null; then
        sed -i 's|$| init=/usr/lib/raspi-config/init_resize.sh|' "$CMDLINE_FILE"
        ok "First-boot resize hook added to $CMDLINE_FILE"
    else
        ok "First-boot resize hook already present in $CMDLINE_FILE"
    fi
else
    warn "cmdline.txt not found – first-boot resize may not be configured"
fi

# ── 13. Fill free space with zeros (maximises .img compression) ──────────────
step "Filling free disk space with zeros for better compression…"
echo "  (This may take a few minutes on a large SD card)"
ZERO_FILE="/ZERO_TEMP_$$"
dd if=/dev/zero of="$ZERO_FILE" bs=1M 2>/dev/null || true
sync
rm -f "$ZERO_FILE"
sync
ok "Free space zeroed"

# ── Done ──────────────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}══════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  ✓ Image preparation complete!${NC}"
echo -e "${GREEN}══════════════════════════════════════════════════════════${NC}"
echo ""
echo "Next steps:"
echo "  1. sudo shutdown -h now"
echo "  2. Remove SD card and create image with USBImager"
echo "     https://bztsrc.gitlab.io/usbimager/"
echo "  3. Optional: shrink with PiShrink"
echo "     sudo pishrink.sh -za power-bridge.img"
echo ""
