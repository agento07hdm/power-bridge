#!/usr/bin/env bash
# =============================================================================
# power-bridge install script for Raspberry Pi OS Lite 32-bit (ARMv6)
# Usage (as root or via sudo):
#   curl -fsSL https://raw.githubusercontent.com/fedzzito/power-bridge/main/install.sh | sudo bash
# =============================================================================
set -euo pipefail

REPO="fedzzito/power-bridge"
BINARY_DEST="/usr/local/bin/power-bridge"
SHARE_DIR="/usr/local/share/power-bridge"
CONFIG_DIR="/etc/power-bridge"
AVAHI_SVC_DIR="/etc/avahi/services"
AP_SSID="ShellyMeter-Setup"
AP_IP="192.168.4.1"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
ok()    { echo -e "${GREEN}✓${NC} $*"; }
warn()  { echo -e "${YELLOW}⚠${NC}  $*"; }
error() { echo -e "${RED}✗${NC} $*" >&2; exit 1; }

# ── Root check ────────────────────────────────────────────────────────────────
[ "$(id -u)" -eq 0 ] || error "Please run as root: sudo bash install.sh"

# ── 1. Determine latest release version ──────────────────────────────────────
echo -e "\n${GREEN}[1/11]${NC} Fetching latest release version…"
VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
[ -n "$VERSION" ] || error "Could not determine latest release version from GitHub API"
ok "Latest version: $VERSION"

# ── 2. Download and install binary ────────────────────────────────────────────
echo -e "\n${GREEN}[2/11]${NC} Downloading binary power-bridge-${VERSION}-linux-armv6…"
BINARY_URL="https://github.com/${REPO}/releases/download/${VERSION}/power-bridge-${VERSION}-linux-armv6"
curl -fsSL "$BINARY_URL" -o "$BINARY_DEST"
chmod 755 "$BINARY_DEST"
ok "Binary installed to $BINARY_DEST"

# Write the installed version to the VERSION file so update.sh can track it
mkdir -p "$CONFIG_DIR"
echo "$VERSION" > "$CONFIG_DIR/VERSION"
# Set the update channel to stable (can be changed to "beta" manually)
[ -f "$CONFIG_DIR/update-channel" ] || echo "stable" > "$CONFIG_DIR/update-channel"
ok "Version $VERSION recorded in $CONFIG_DIR/VERSION"

# ── 3. System packages ────────────────────────────────────────────────────────
echo -e "\n${GREEN}[3/11]${NC} Installing required packages…"
apt-get update -qq
apt-get install -y --no-install-recommends \
    hostapd \
    dnsmasq \
    avahi-daemon \
    curl
systemctl unmask hostapd 2>/dev/null || true
systemctl unmask dnsmasq 2>/dev/null || true
ok "Packages installed"

# ── 4. Config directory & default config ─────────────────────────────────────
echo -e "\n${GREEN}[4/11]${NC} Setting up config directory…"
mkdir -p "$CONFIG_DIR"
if [ ! -f "$CONFIG_DIR/config.yaml" ]; then
    cat > "$CONFIG_DIR/config.yaml" << 'EOF'
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
    ok "Default config written to $CONFIG_DIR/config.yaml"
else
    ok "Config already exists – not overwriting $CONFIG_DIR/config.yaml"
fi

# ── 5. systemd service (embedded heredoc) ────────────────────────────────────
echo -e "\n${GREEN}[5/11]${NC} Installing systemd service…"
cat > /etc/systemd/system/power-bridge.service << 'EOF'
[Unit]
Description=power-bridge – powerfox poweropti → virtual Shelly Pro 3EM
Documentation=https://github.com/fedzzito/power-bridge
# Start only after the boot-time OTA update check has completed.
# Wants= (not Requires=) so that power-bridge starts even if the update
# service is not installed or fails unexpectedly.
After=network-online.target power-bridge-update.service
Wants=network-online.target power-bridge-update.service

[Service]
Type=simple
User=root
AmbientCapabilities=CAP_NET_BIND_SERVICE
ExecStart=/usr/local/bin/power-bridge \
    -config /etc/power-bridge/config.yaml \
    -listen :80
Restart=on-failure
RestartSec=5s

StandardOutput=journal
StandardError=journal
SyslogIdentifier=power-bridge

NoNewPrivileges=yes
ProtectSystem=full
ProtectHome=true
PrivateTmp=yes
ReadWritePaths=/etc/power-bridge /etc/wpa_supplicant

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable power-bridge.service
ok "Systemd service installed and enabled"

# ── 6. Avahi service (embedded heredoc) ──────────────────────────────────────
echo -e "\n${GREEN}[6/11]${NC} Registering mDNS service with Avahi…"
mkdir -p "$AVAHI_SVC_DIR"
cat > "$AVAHI_SVC_DIR/power-bridge.service" << 'EOF'
<?xml version="1.0" standalone='no'?>
<!DOCTYPE service-group SYSTEM "avahi-service.dtd">
<service-group>
  <name replace-wildcards="yes">shellypro3em-%h</name>
  <service>
    <type>_http._tcp</type>
    <port>80</port>
    <txt-record>path=/</txt-record>
  </service>
  <service>
    <type>_shelly._tcp</type>
    <port>80</port>
    <txt-record>gen=2</txt-record>
    <txt-record>app=Pro3EM</txt-record>
    <txt-record>ver=2.2.1</txt-record>
    <txt-record>id=shellypro3em-aabbccddeeff</txt-record>
    <txt-record>mac=AABBCCDDEEFF</txt-record>
  </service>
</service-group>
EOF
systemctl enable avahi-daemon
systemctl restart avahi-daemon 2>/dev/null || true
ok "Avahi mDNS service registered"

# ── 7. hostapd + dnsmasq (AP mode, only if not already configured) ───────────
echo -e "\n${GREEN}[7/11]${NC} Configuring Access Point (hostapd + dnsmasq)…"

if [ ! -f /etc/hostapd/hostapd.conf ] || ! grep -q "power-bridge" /etc/hostapd/hostapd.conf 2>/dev/null; then
    cat > /etc/hostapd/hostapd.conf << EOF
# power-bridge AP config
interface=wlan0
driver=nl80211
ssid=${AP_SSID}
hw_mode=g
channel=6
wmm_enabled=0
macaddr_acl=0
auth_algs=1
ignore_broadcast_ssid=0
wpa=0
EOF
    sed -i 's|^#\?DAEMON_CONF=.*|DAEMON_CONF="/etc/hostapd/hostapd.conf"|' /etc/default/hostapd 2>/dev/null || \
        echo 'DAEMON_CONF="/etc/hostapd/hostapd.conf"' >> /etc/default/hostapd
    ok "hostapd configured for SSID '$AP_SSID'"
else
    ok "hostapd already configured – skipping"
fi

if [ ! -f /etc/dnsmasq.d/power-bridge-ap.conf ]; then
    cat > /etc/dnsmasq.d/power-bridge-ap.conf << EOF
# power-bridge AP mode DHCP
interface=wlan0
dhcp-range=192.168.4.10,192.168.4.50,255.255.255.0,24h
address=/#/${AP_IP}
EOF
    ok "dnsmasq DHCP configured"
else
    ok "dnsmasq already configured – skipping"
fi

if ! grep -q "power-bridge AP" /etc/dhcpcd.conf 2>/dev/null; then
    cat >> /etc/dhcpcd.conf << EOF

# power-bridge AP mode static IP
interface wlan0
    static ip_address=${AP_IP}/24
    nohook wpa_supplicant
EOF
    ok "Static IP configured for wlan0"
else
    ok "dhcpcd static IP already configured – skipping"
fi

# ── 8. Install prepare-image.sh ───────────────────────────────────────────────
echo -e "\n${GREEN}[8/11]${NC} Installing prepare-image.sh…"
mkdir -p "$SHARE_DIR"
cat > "$SHARE_DIR/prepare-image.sh" << 'PREPARE_EOF'
#!/usr/bin/env bash
# =============================================================================
# power-bridge prepare-image.sh
# Run on the Pi BEFORE creating a SD-card image for redistribution.
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

step "Stopping power-bridge services…"
systemctl stop power-bridge.service 2>/dev/null || true
systemctl stop power-bridge-update.service 2>/dev/null || true
ok "Services stopped"

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
echo "stable" > /etc/power-bridge/update-channel 2>/dev/null || true

step "Removing SSH host keys…"
rm -f /etc/ssh/ssh_host_*
if [ -f /lib/systemd/system/regenerate_ssh_host_keys.service ]; then
    systemctl enable regenerate_ssh_host_keys 2>/dev/null || true
fi
ok "SSH host keys removed (will regenerate on first boot)"

step "Clearing machine-ID…"
truncate -s 0 /etc/machine-id
rm -f /var/lib/dbus/machine-id
ok "Machine-ID cleared"

step "Cleaning apt cache…"
apt-get clean -y 2>/dev/null || true
apt-get autoremove -y --purge 2>/dev/null || true
rm -rf /var/lib/apt/lists/*
ok "apt cache cleaned"

step "Cleaning pip/npm cache…"
pip3 cache purge 2>/dev/null || true
pip cache purge 2>/dev/null || true
rm -rf /root/.cache/pip /home/pi/.cache/pip 2>/dev/null || true
if command -v npm > /dev/null 2>&1; then npm cache clean --force 2>/dev/null || true; fi
rm -rf /root/.npm /home/pi/.npm 2>/dev/null || true
ok "pip/npm cache cleaned"

step "Removing temporary files…"
rm -rf /tmp/* /var/tmp/* 2>/dev/null || true
rm -rf /root/.cache/go-build /home/pi/.cache/go-build 2>/dev/null || true
ok "Temporary files removed"

step "Cleaning log files…"
journalctl --vacuum-time=1s 2>/dev/null || true
find /var/log -type f \( -name "*.log" -o -name "syslog" -o -name "messages" \
    -o -name "auth.log" -o -name "kern.log" \) \
    -exec truncate -s 0 {} \; 2>/dev/null || true
find /var/log -type f -name "*.gz" -delete 2>/dev/null || true
find /var/log -type f -name "*.[0-9]" -delete 2>/dev/null || true
ok "Log files cleaned"

step "Removing DHCP leases…"
rm -f /var/lib/dhcp/*.leases /var/lib/dhcpcd5/*.lease /var/lib/dhcpcd/*.lease 2>/dev/null || true
ok "DHCP leases removed"

step "Clearing shell history…"
rm -f /root/.bash_history /home/pi/.bash_history 2>/dev/null || true
history -c 2>/dev/null || true
ok "Shell history cleared"

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
        ok "First-boot resize hook already present"
    fi
else
    warn "cmdline.txt not found – first-boot resize may not be configured"
fi

step "Filling free disk space with zeros for better compression…"
echo "  (This may take a few minutes on a large SD card)"
ZERO_FILE="/ZERO_TEMP_$$"
dd if=/dev/zero of="$ZERO_FILE" bs=1M 2>/dev/null || true
sync
rm -f "$ZERO_FILE"
sync
ok "Free space zeroed"

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
PREPARE_EOF
chmod 755 "$SHARE_DIR/prepare-image.sh"
ok "prepare-image.sh installed to $SHARE_DIR/prepare-image.sh"

# ── 9. Install OTA update scripts ─────────────────────────────────────────────
echo -e "\n${GREEN}[9/11]${NC} Installing OTA update scripts…"
mkdir -p "$SHARE_DIR"

cat > "$SHARE_DIR/update.sh" << 'UPDATE_EOF'
#!/usr/bin/env bash
# =============================================================================
# power-bridge update.sh – boot-time OTA update via GitHub Releases
# Called automatically by power-bridge-update.service at each boot.
# Manual usage: sudo bash /usr/local/share/power-bridge/update.sh
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

log()  { echo "[$(date '+%Y-%m-%d %H:%M:%S')]  $*"; logger -t "$LOG_TAG" "$*" 2>/dev/null || true; }
ok()   { log "✓ $*"; }
warn() { log "⚠ $*"; }
err()  { log "✗ $*" >&2; }

[ "$(id -u)" -eq 0 ] || { err "Please run as root"; exit 1; }

CHANNEL="stable"
[ -f "$CHANNEL_FILE" ] && CHANNEL=$(tr -d '[:space:]' < "$CHANNEL_FILE")

INSTALLED_VERSION="unknown"
[ -f "$VERSION_FILE" ] && INSTALLED_VERSION=$(tr -d '[:space:]' < "$VERSION_FILE")

log "power-bridge update check started | installed=$INSTALLED_VERSION channel=$CHANNEL"

# Check internet connectivity (fast, fail-safe)
if ! curl -fsSL --max-time 5 --head "https://api.github.com" > /dev/null 2>&1; then
    warn "No internet connection – skipping update, continuing with $INSTALLED_VERSION"
    exit 0
fi

# Fetch latest release metadata from GitHub API
LATEST_VERSION=""
CHANGELOG=""

if [ "$CHANNEL" = "stable" ]; then
    RELEASE_JSON=$(curl -fsSL --max-time 15 \
        "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null) || {
        warn "GitHub API unreachable – skipping update"; exit 0
    }
    LATEST_VERSION=$(echo "$RELEASE_JSON" \
        | grep '"tag_name"' | head -1 \
        | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
    CHANGELOG=$(echo "$RELEASE_JSON" \
        | grep '"body"' | head -1 \
        | sed 's/.*"body": *"\(.*\)".*/\1/' \
        | sed 's/\\n/\n/g; s/\\r//g' | head -5) || CHANGELOG=""
else
    ALL_JSON=$(curl -fsSL --max-time 15 \
        "https://api.github.com/repos/${REPO}/releases" 2>/dev/null) || {
        warn "GitHub API unreachable – skipping update"; exit 0
    }
    # First pre-release, or fall back to latest stable
    LATEST_VERSION=$(echo "$ALL_JSON" \
        | awk '/"prerelease": true/{found=1} found && /"tag_name"/{
            gsub(/.*"tag_name": *"/, ""); gsub(/".*/, ""); print; exit}')
    if [ -z "$LATEST_VERSION" ]; then
        LATEST_VERSION=$(echo "$ALL_JSON" \
            | grep '"tag_name"' | head -1 \
            | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
    fi
fi

if [ -z "$LATEST_VERSION" ]; then
    warn "Could not determine latest version – skipping update"; exit 0
fi

log "Latest available version: $LATEST_VERSION"
[ -n "$CHANGELOG" ] && log "Release notes: $CHANGELOG"

if [ "$INSTALLED_VERSION" = "$LATEST_VERSION" ]; then
    ok "Already up to date ($INSTALLED_VERSION) – no update needed"; exit 0
fi

log "Update available: $INSTALLED_VERSION → $LATEST_VERSION"

BINARY_NAME="power-bridge-${LATEST_VERSION}-linux-armv6"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${LATEST_VERSION}/${BINARY_NAME}"
TEMP_BINARY=$(mktemp /tmp/power-bridge-update-XXXXXX)

trap 'rm -f "$TEMP_BINARY" "${BINARY_DEST}.tmp" 2>/dev/null || true' EXIT

log "Downloading $DOWNLOAD_URL …"
if ! curl -fsSL --max-time 120 "$DOWNLOAD_URL" -o "$TEMP_BINARY"; then
    err "Download failed: $DOWNLOAD_URL – keeping current version $INSTALLED_VERSION"
    exit 0
fi

if [ ! -s "$TEMP_BINARY" ]; then
    err "Downloaded file is empty – aborting update"; exit 0
fi

chmod 755 "$TEMP_BINARY"
ok "Binary downloaded ($(du -sh "$TEMP_BINARY" | cut -f1))"

# Backup current binary before replacing
mkdir -p "$SHARE_DIR"
[ -f "$BINARY_DEST" ] && cp -f "$BINARY_DEST" "$BACKUP_BINARY" && ok "Backup saved"
[ -f "$VERSION_FILE" ] && cp -f "$VERSION_FILE" "$BACKUP_VERSION"

# Atomic install: copy to .tmp, then rename (robust against power failure)
cp -f "$TEMP_BINARY" "${BINARY_DEST}.tmp"
mv -f "${BINARY_DEST}.tmp" "$BINARY_DEST"
chmod 755 "$BINARY_DEST"

mkdir -p "$(dirname "$VERSION_FILE")"
echo "$LATEST_VERSION" > "$VERSION_FILE"

ok "Update complete: $INSTALLED_VERSION → $LATEST_VERSION"
log "power-bridge update finished | version=$LATEST_VERSION"
exit 0
UPDATE_EOF
chmod 755 "$SHARE_DIR/update.sh"
ok "update.sh installed to $SHARE_DIR/update.sh"

cat > "$SHARE_DIR/rollback.sh" << 'ROLLBACK_EOF'
#!/usr/bin/env bash
# =============================================================================
# power-bridge rollback.sh – restore previous binary after a failed update
# Usage: sudo bash /usr/local/share/power-bridge/rollback.sh
# =============================================================================
set -euo pipefail

BINARY_DEST="/usr/local/bin/power-bridge"
SHARE_DIR="/usr/local/share/power-bridge"
VERSION_FILE="/etc/power-bridge/VERSION"
BACKUP_BINARY="${SHARE_DIR}/power-bridge.bak"
BACKUP_VERSION="${SHARE_DIR}/VERSION.bak"
LOG_TAG="power-bridge-update"

log()  { echo "[$(date '+%Y-%m-%d %H:%M:%S')]  $*"; logger -t "$LOG_TAG" "$*" 2>/dev/null || true; }
ok()   { log "✓ $*"; }
err()  { log "✗ $*" >&2; }

[ "$(id -u)" -eq 0 ] || { err "Please run as root"; exit 1; }

[ -f "$BACKUP_BINARY" ] || { err "No backup found at $BACKUP_BINARY – cannot rollback"; exit 1; }

CURRENT_VERSION="unknown"
[ -f "$VERSION_FILE" ] && CURRENT_VERSION=$(tr -d '[:space:]' < "$VERSION_FILE")
BACKUP_VER="unknown"
[ -f "$BACKUP_VERSION" ] && BACKUP_VER=$(tr -d '[:space:]' < "$BACKUP_VERSION")

log "Rolling back: $CURRENT_VERSION → $BACKUP_VER"

systemctl stop power-bridge.service 2>/dev/null || true
sleep 1

cp -f "$BACKUP_BINARY" "${BINARY_DEST}.tmp"
mv -f "${BINARY_DEST}.tmp" "$BINARY_DEST"
chmod 755 "$BINARY_DEST"
ok "Binary restored from backup"

[ -f "$BACKUP_VERSION" ] && cp -f "$BACKUP_VERSION" "$VERSION_FILE" && ok "VERSION restored to $BACKUP_VER"

rm -f "$BACKUP_BINARY" "$BACKUP_VERSION"

systemctl start power-bridge.service 2>/dev/null || { err "Failed to start power-bridge.service"; exit 1; }
ok "Rollback complete – running $BACKUP_VER"
log "power-bridge rollback finished | version=$BACKUP_VER"
ROLLBACK_EOF
chmod 755 "$SHARE_DIR/rollback.sh"
ok "rollback.sh installed to $SHARE_DIR/rollback.sh"

# ── 10. Install power-bridge-update.service ──────────────────────────────────
echo -e "\n${GREEN}[10/11]${NC} Installing power-bridge-update.service…"
cat > /etc/systemd/system/power-bridge-update.service << 'EOF'
[Unit]
Description=power-bridge OTA update – check GitHub Releases at boot
Documentation=https://github.com/fedzzito/power-bridge
Before=power-bridge.service
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/local/share/power-bridge/update.sh
TimeoutStartSec=180
SuccessExitStatus=0 1

StandardOutput=journal
StandardError=journal
SyslogIdentifier=power-bridge-update

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable power-bridge-update.service
ok "power-bridge-update.service installed and enabled"

# ── 11. Enable and start services ─────────────────────────────────────────────
echo -e "\n${GREEN}[11/11]${NC} Starting services…"
systemctl restart dhcpcd 2>/dev/null || true
systemctl restart hostapd 2>/dev/null || warn "hostapd failed to start (may need reboot)"
systemctl restart dnsmasq 2>/dev/null || warn "dnsmasq failed to start"
systemctl start power-bridge.service 2>/dev/null || warn "power-bridge failed to start"

# ── Done ──────────────────────────────────────────────────────────────────────
IP=$(hostname -I 2>/dev/null | awk '{print $1}' || echo "unknown")

echo ""
echo -e "${GREEN}╔══════════════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║  power-bridge ${VERSION} installed successfully!             ${NC}"
echo -e "${GREEN}╠══════════════════════════════════════════════════════════════╣${NC}"
echo -e "${GREEN}║  Access Point: ${AP_SSID}                     ${NC}"
echo -e "${GREEN}║  Setup URL:    http://${AP_IP}                         ${NC}"
echo -e "${GREEN}║  Device IP:    http://${IP}                           ${NC}"
echo -e "${GREEN}╠══════════════════════════════════════════════════════════════╣${NC}"
echo -e "${GREEN}║  OTA updates:  automatic at every boot (stable channel)      ${NC}"
echo -e "${GREEN}║  Logs:         journalctl -u power-bridge-update             ${NC}"
echo -e "${GREEN}║  Rollback:     sudo bash $SHARE_DIR/rollback.sh${NC}"
echo -e "${GREEN}╚══════════════════════════════════════════════════════════════╝${NC}"
