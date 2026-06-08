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
#        pishrink.sh -zas power-bridge.img
#        (-s skips the auto-resize hook; required for Bookworm to avoid boot loop)
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

# ── 2b. Clear WiFi credentials – all storage locations ───────────────────────
step "Clearing WiFi credentials from all storage locations…"

# 1. wpa_supplicant (Bullseye and older, or legacy fallback)
mkdir -p /etc/wpa_supplicant
cat > /etc/wpa_supplicant/wpa_supplicant.conf << 'EOF'
country=DE
ctrl_interface=DIR=/var/run/wpa_supplicant GROUP=netdev
update_config=1
EOF
chmod 600 /etc/wpa_supplicant/wpa_supplicant.conf
ok "wpa_supplicant.conf cleared"

# 2. NetworkManager connection profiles (Bookworm / NM-managed systems)
#    NM stores the PSK in plain text inside .nmconnection files.
#    Only WiFi station-mode profiles are removed; AP profiles (mode=ap)
#    such as power-bridge-ap are preserved so the first-boot hotspot works.
NM_DIR="/etc/NetworkManager/system-connections"
if [ -d "$NM_DIR" ]; then
    REMOVED=0
    for f in "$NM_DIR"/*.nmconnection "$NM_DIR"/*.conf "$NM_DIR"/*; do
        [ -f "$f" ] || continue
        # Skip AP-mode profiles – they contain no user credentials
        if grep -q "mode=ap" "$f" 2>/dev/null; then
            continue
        fi
        # Remove any WiFi station-mode profile
        if grep -q "type=wifi" "$f" 2>/dev/null; then
            rm -f "$f"
            REMOVED=$((REMOVED + 1))
        fi
    done
    if [ "$REMOVED" -gt 0 ]; then
        # Reload NM so it drops in-memory cached secrets
        nmcli connection reload 2>/dev/null || true
        ok "Removed $REMOVED NetworkManager WiFi profile(s) from $NM_DIR"
    else
        ok "No NetworkManager WiFi station profiles found in $NM_DIR"
    fi
fi

# 3. firstrun.sh on the boot partition (written by Raspberry Pi Imager)
#    This file may contain the SSID and password entered during flashing.
#    It is consumed and left on disk by the firstboot mechanism – remove it.
for BOOT_DIR in /boot/firmware /boot; do
    if [ -f "$BOOT_DIR/firstrun.sh" ]; then
        rm -f "$BOOT_DIR/firstrun.sh"
        # Also remove the firstboot init= hook from cmdline.txt so the next
        # first-boot does not try to run a now-missing firstrun.sh.
        for CMDLINE in "$BOOT_DIR/cmdline.txt"; do
            [ -f "$CMDLINE" ] || continue
            sed -i 's| systemd.run=[^ ]*||g' "$CMDLINE" 2>/dev/null || true
            sed -i 's| init=[^ ]*firstboot[^ ]*||g' "$CMDLINE" 2>/dev/null || true
            sed -i 's| init=[^ ]*init_resize[^ ]*||g' "$CMDLINE" 2>/dev/null || true
        done
        ok "Removed firstrun.sh (and firstboot hook) from $BOOT_DIR"
    fi
done

# 4. Verify: report any remaining files that still mention a PSK / password
echo ""
echo "  Verifying no credentials remain on disk…"
LEAKS=0
for CHECK_FILE in \
        /etc/wpa_supplicant/wpa_supplicant.conf \
        /etc/NetworkManager/system-connections/*.nmconnection \
        /etc/NetworkManager/system-connections/*.conf \
        /boot/firmware/firstrun.sh /boot/firstrun.sh; do
    [ -f "$CHECK_FILE" ] || continue
    if grep -qiE "psk=.+|password=.+|wifi_password: .+" "$CHECK_FILE" 2>/dev/null; then
        warn "POSSIBLE CREDENTIAL LEAK in: $CHECK_FILE"
        LEAKS=$((LEAKS + 1))
    fi
done
if [ "$LEAKS" -eq 0 ]; then
    ok "Verification passed – no credentials found in checked files"
else
    warn "$LEAKS file(s) may still contain credentials – review before imaging!"
fi

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
find /var/log -type f -name "*.gz" -delete 2>/dev/null ||