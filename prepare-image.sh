#!/usr/bin/env bash
# =============================================================================
# power-bridge prepare-image.sh
# Run on the Pi BEFORE pulling a SD-card image for mass production.
# Usage: sudo bash /usr/local/share/power-bridge/prepare-image.sh
# =============================================================================
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
ok()    { echo -e "${GREEN}✓${NC} $*"; }
error() { echo -e "${RED}✗${NC} $*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || error "Please run as root: sudo bash prepare-image.sh"

echo "Preparing Pi for image cloning…"

# Stop power-bridge service
systemctl stop power-bridge 2>/dev/null || true
ok "power-bridge service stopped"

# Reset config to unconfigured defaults
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

# Remove SSH host keys (will be regenerated on first boot)
rm -f /etc/ssh/ssh_host_*
touch /etc/ssh/sshd_not_to_be_run 2>/dev/null || true
# Re-enable host key regeneration on next boot (Raspberry Pi OS Bookworm)
if [ -f /lib/systemd/system/regenerate_ssh_host_keys.service ]; then
    systemctl enable regenerate_ssh_host_keys 2>/dev/null || true
fi
ok "SSH host keys removed (will regenerate on first boot)"

# Clear machine-id
truncate -s 0 /etc/machine-id
rm -f /var/lib/dbus/machine-id
ok "Machine-ID cleared"

# Clean logs
journalctl --vacuum-time=1s 2>/dev/null || true
find /var/log -type f -name "*.log" -delete 2>/dev/null || true
find /var/log -type f -name "*.gz"  -delete 2>/dev/null || true
find /var/log -type f               -exec truncate -s 0 {} \; 2>/dev/null || true
ok "Log files cleaned"

# Remove DHCP leases
rm -f /var/lib/dhcp/*.leases 2>/dev/null || true
rm -f /var/lib/dhcpcd5/*.lease 2>/dev/null || true
ok "DHCP leases removed"

# Clear bash history
rm -f /root/.bash_history ~/.bash_history 2>/dev/null || true
history -c 2>/dev/null || true
ok "Bash history cleared"

echo ""
echo -e "${GREEN}✓ Image ist bereit.${NC}"
echo "Fahre den Pi jetzt herunter: sudo shutdown -h now"
