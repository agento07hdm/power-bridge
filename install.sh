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
AP_SSID="power-bridge"
AP_IP="192.168.4.1"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
ok()    { echo -e "${GREEN}✓${NC} $*"; }
warn()  { echo -e "${YELLOW}⚠${NC}  $*"; }
error() { echo -e "${RED}✗${NC} $*" >&2; exit 1; }

# ── Root check ────────────────────────────────────────────────────────────────
[ "$(id -u)" -eq 0 ] || error "Please run as root: sudo bash install.sh"

# ── 1. Determine latest release version ──────────────────────────────────────
echo -e "\n${GREEN}[1/13]${NC} Fetching latest release version…"
VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
[ -n "$VERSION" ] || error "Could not determine latest release version from GitHub API"
ok "Latest version: $VERSION"

# Stop running service before replacing binary (fixes locked-binary update issue)
systemctl stop power-bridge 2>/dev/null || true

# ── 2. Download and install binary ────────────────────────────────────────────
echo -e "\n${GREEN}[2/13]${NC} Downloading binary power-bridge-${VERSION}-linux-armv6…"
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
echo -e "\n${GREEN}[3/13]${NC} Installing required packages…"
apt-get update -qq
apt-get install -y --no-install-recommends \
    avahi-daemon \
    curl
if ! command -v nmcli >/dev/null 2>&1; then
    apt-get install -y --no-install-recommends hostapd dnsmasq
    systemctl unmask hostapd 2>/dev/null || true
    systemctl unmask dnsmasq 2>/dev/null || true
    warn "nmcli not found: installed hostapd/dnsmasq fallback stack"
fi
ok "Packages installed"

# ── 4. Config directory & default config ─────────────────────────────────────
echo -e "\n${GREEN}[4/13]${NC} Setting up config directory…"
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
echo -e "\n${GREEN}[5/13]${NC} Installing systemd service…"
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
ReadWritePaths=/etc/power-bridge /etc/wpa_supplicant /etc/NetworkManager/system-connections

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable power-bridge.service
ok "Systemd service installed and enabled"

# ── 6. Avahi service (embedded heredoc) ──────────────────────────────────────
echo -e "\n${GREEN}[6/13]${NC} Registering mDNS service with Avahi…"
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

# ── 7. NetworkManager WLAN management ─────────────────────────────────────────
echo -e "\n${GREEN}[7/13]${NC} Configuring wlan0 under NetworkManager…"
if command -v nmcli >/dev/null 2>&1 && systemctl is-active NetworkManager >/dev/null 2>&1; then
    rm -f /etc/NetworkManager/conf.d/power-bridge-unmanaged.conf
    systemctl reload NetworkManager 2>/dev/null || true

    NM_SSID=$(nmcli -g 802-11-wireless.ssid connection show preconfigured 2>/dev/null || true)

    nmcli connection delete power-bridge-ap 2>/dev/null || true
    nmcli connection delete power-bridge-wifi 2>/dev/null || true

    if [ -n "$NM_SSID" ]; then
        if ! nmcli connection clone preconfigured power-bridge-wifi >/dev/null 2>&1; then
            NM_KEYMGMT=$(nmcli -g 802-11-wireless-security.key-mgmt connection show preconfigured 2>/dev/null || true)
            # Clone failure fallback: read PSK once so WPA networks can still be migrated.
            NM_PSK=$(nmcli -s -g 802-11-wireless-security.psk connection show preconfigured 2>/dev/null || true)
            if [ "$NM_KEYMGMT" = "wpa-psk" ] && [ -n "$NM_PSK" ]; then
                nmcli connection add type wifi ifname wlan0 con-name power-bridge-wifi ssid "$NM_SSID" \
                    wifi-sec.key-mgmt wpa-psk wifi-sec.psk "$NM_PSK" 2>/dev/null || true
            elif [ -z "$NM_KEYMGMT" ] || [ "$NM_KEYMGMT" = "none" ]; then
                nmcli connection add type wifi ifname wlan0 con-name power-bridge-wifi ssid "$NM_SSID" \
                    2>/dev/null || true
            else
                warn "Unsupported preconfigured key-mgmt '$NM_KEYMGMT' – creating open fallback profile"
                nmcli connection add type wifi ifname wlan0 con-name power-bridge-wifi ssid "$NM_SSID" \
                    2>/dev/null || true
            fi
            unset NM_PSK
        fi
        nmcli connection modify power-bridge-wifi connection.interface-name wlan0 802-11-wireless.ssid "$NM_SSID" 2>/dev/null || true
        nmcli connection delete preconfigured 2>/dev/null || true
        nmcli connection up power-bridge-wifi 2>/dev/null || warn "Could not activate power-bridge-wifi"
        ok "NetworkManager now manages wlan0 with profile power-bridge-wifi"
    else
        nmcli connection add type wifi ifname wlan0 con-name power-bridge-ap ssid "$AP_SSID" \
            802-11-wireless.mode ap 802-11-wireless.band bg \
            ipv4.method shared ipv4.addresses ${AP_IP}/24 || true
        nmcli connection up power-bridge-ap 2>/dev/null || warn "Could not activate power-bridge-ap"
        ok "No preconfigured WiFi found – AP mode prepared via NetworkManager"
    fi
else
    ok "nmcli/NetworkManager unavailable – keeping legacy wpa_supplicant flow"
fi

# ── 8. AP stack handling ──────────────────────────────────────────────────────
echo -e "\n${GREEN}[8/13]${NC} Handling AP stack services…"
if command -v nmcli >/dev/null 2>&1 && systemctl is-active NetworkManager >/dev/null 2>&1; then
    systemctl disable hostapd dnsmasq 2>/dev/null || true
    systemctl stop hostapd dnsmasq 2>/dev/null || true
    ok "hostapd/dnsmasq disabled under NetworkManager mode"
else
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
fi

# ── 9. Install prepare-image.sh ───────────────────────────────────────────────
echo -e "\n${GREEN}[9/13]${NC} Installing prepare-image.sh…"
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

# 2. NetworkManager connection profiles (Bookworm / NM-managed systems).
#    NM stores the PSK in plain text inside .nmconnection files.
#    AP-mode profiles (mode=ap) are preserved so the first-boot hotspot works.
NM_DIR="/etc/NetworkManager/system-connections"
if [ -d "$NM_DIR" ]; then
    REMOVED=0
    for f in "$NM_DIR"/*.nmconnection "$NM_DIR"/*.conf "$NM_DIR"/*; do
        [ -f "$f" ] || continue
        grep -q "mode=ap" "$f" 2>/dev/null && continue
        if grep -q "type=wifi" "$f" 2>/dev/null; then
            rm -f "$f"
            REMOVED=$((REMOVED + 1))
        fi
    done
    if [ "$REMOVED" -gt 0 ]; then
        nmcli connection reload 2>/dev/null || true
        ok "Removed $REMOVED NetworkManager WiFi profile(s) from $NM_DIR"
    else
        ok "No NetworkManager WiFi station profiles found in $NM_DIR"
    fi
fi

# 3. firstrun.sh on the boot partition (written by Raspberry Pi Imager).
#    This file may contain the SSID and password entered during flashing.
for BOOT_DIR in /boot/firmware /boot; do
    if [ -f "$BOOT_DIR/firstrun.sh" ]; then
        rm -f "$BOOT_DIR/firstrun.sh"
        for CMDLINE in "$BOOT_DIR/cmdline.txt"; do
            [ -f "$CMDLINE" ] || continue
            sed -i 's| systemd.run=[^ ]*||g' "$CMDLINE" 2>/dev/null || true
            sed -i 's| init=[^ ]*firstboot[^ ]*||g' "$CMDLINE" 2>/dev/null || true
        done
        ok "Removed firstrun.sh (and firstboot hook) from $BOOT_DIR"
    fi
done

# 4. boot-counter reset (so the first user does not trigger a spurious reset).
rm -f /etc/power-bridge/boot-counter 2>/dev/null || true
ok "boot-counter cleared"

# 5. Verify: report any remaining files that still mention a PSK / password.
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

step "Removing boot-time hooks from cmdline.txt…"
# Remove the raspi-config resize hook and Pi Imager firstrun hooks.
# The resize hook causes a boot loop when the image is re-flashed because
# the PARTUUID no longer matches on the new card (Bookworm issue).
for CMDLINE_FILE in /boot/firmware/cmdline.txt /boot/cmdline.txt; do
    [ -f "$CMDLINE_FILE" ] || continue
    sed -i 's| systemd.run=[^ ]*||g' "$CMDLINE_FILE" 2>/dev/null || true
    sed -i 's| init=[^ ]*firstboot[^ ]*||g' "$CMDLINE_FILE" 2>/dev/null || true
    sed -i 's| init=[^ ]*init_resize[^ ]*||g' "$CMDLINE_FILE" 2>/dev/null || true
    ok "Boot hooks removed from $CMDLINE_FILE"
done

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

# ── 10. Install OTA update scripts ────────────────────────────────────────────
echo -e "\n${GREEN}[10/13]${NC} Installing OTA update scripts…"
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

# ── 11. Install boot-watchdog (Stecker-Ziehen Reset) ─────────────────────────
echo -e "\n${GREEN}[11/13]${NC} Installing boot-watchdog…"
mkdir -p "$SHARE_DIR"

cat > "$SHARE_DIR/boot-watchdog.sh" << 'WATCHDOG_EOF'
#!/usr/bin/env bash
# =============================================================================
# power-bridge boot-watchdog.sh
#
# Counts rapid successive reboots. After MAX_RESETS boots within STABLE_SECS
# seconds the Pi forces AP mode and clears WiFi credentials.
#
# User procedure: pull the power plug 3 times, each time waiting ~15 seconds
# (enough for the Pi to boot and write the counter). The green ACT LED blinks
# N times after each rapid boot to confirm the count was registered:
#   1 blink  = first rapid boot counted  (2 more needed)
#   2 blinks = second rapid boot counted (1 more needed)
#   10 rapid blinks = reset triggered, AP mode active
#
# Called by power-bridge-boot-watchdog.service at every boot.
# =============================================================================
COUNTER_FILE="/etc/power-bridge/boot-counter"
CONFIG_FILE="/etc/power-bridge/config.yaml"
AP_SSID="power-bridge"
AP_IP="192.168.4.1"
MAX_RESETS=3
STABLE_SECS=60

# ── LED helper ────────────────────────────────────────────────────────────────
# Blinks the Pi ACT/activity LED N times, then restores the original trigger.
# Works on Pi Zero W (led0) and Pi Zero 2 W / Pi 4 (ACT). Silently skipped
# when the LED sysfs path is not available.
blink_led() {
    local n=$1 delay=${2:-0.25}
    local led=""
    for path in /sys/class/leds/ACT /sys/class/leds/led0 /sys/class/leds/activity; do
        [ -d "$path" ] && led="$path" && break
    done
    [ -n "$led" ] || return 0
    local orig_trigger
    orig_trigger=$(cat "$led/trigger" 2>/dev/null | grep -oP '(?<=\[)[^\]]+' || echo "mmc0")
    echo none > "$led/trigger" 2>/dev/null || return 0
    local i
    for i in $(seq 1 "$n"); do
        echo 1 > "$led/brightness" 2>/dev/null
        sleep "$delay"
        echo 0 > "$led/brightness" 2>/dev/null
        sleep "$delay"
    done
    echo "$orig_trigger" > "$led/trigger" 2>/dev/null || true
}

# Read current counter (default 0), increment, and persist immediately.
COUNT=$(cat "$COUNTER_FILE" 2>/dev/null | tr -cd '0-9')
COUNT=${COUNT:-0}
COUNT=$((COUNT + 1))
echo "$COUNT" > "$COUNTER_FILE"

logger -t power-bridge-watchdog "boot-counter=$COUNT threshold=$MAX_RESETS stable=${STABLE_SECS}s"

if [ "$COUNT" -ge "$MAX_RESETS" ]; then
    logger -t power-bridge-watchdog "Reset triggered (${COUNT} rapid boots) – forcing AP mode"
    echo "0" > "$COUNTER_FILE"

    # Reset config to unconfigured defaults.
    if [ -f "$CONFIG_FILE" ]; then
        cat > "$CONFIG_FILE" << 'CFGEOF'
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
CFGEOF
    fi

    # Clear NetworkManager WiFi station profiles.
    if command -v nmcli >/dev/null 2>&1; then
        nmcli connection delete power-bridge-wifi 2>/dev/null || true
        nmcli connection show power-bridge-ap >/dev/null 2>&1 || \
            nmcli connection add type wifi ifname wlan0 con-name power-bridge-ap \
                ssid "$AP_SSID" 802-11-wireless.mode ap \
                802-11-wireless.band bg ipv4.method shared \
                ipv4.addresses "$AP_IP/24" 2>/dev/null || true
        nmcli connection modify power-bridge-ap connection.autoconnect yes 2>/dev/null || true
        nmcli connection up power-bridge-ap 2>/dev/null || true
    else
        systemctl stop wpa_supplicant@wlan0 2>/dev/null || true
        systemctl stop wpa_supplicant 2>/dev/null || true
        systemctl start hostapd 2>/dev/null || true
        systemctl start dnsmasq 2>/dev/null || true
    fi

    # 10 rapid blinks = reset done, AP active.
    blink_led 10 0.1

    sleep 3
    systemctl restart power-bridge 2>/dev/null || true
    logger -t power-bridge-watchdog "AP reset complete – SSID: $AP_SSID (http://$AP_IP)"
    exit 0
fi

# Not yet at threshold: blink COUNT times so the user sees the count was registered.
blink_led "$COUNT"

# Sleep until the stable window expires, then clear counter.
sleep "$STABLE_SECS"
echo "0" > "$COUNTER_FILE"
logger -t power-bridge-watchdog "Stable after ${STABLE_SECS}s – counter reset"
WATCHDOG_EOF
chmod +x "$SHARE_DIR/boot-watchdog.sh"

cat > /etc/systemd/system/power-bridge-boot-watchdog.service << 'EOF'
[Unit]
Description=power-bridge boot watchdog (Stecker-Ziehen Reset)
After=power-bridge.service
BindsTo=power-bridge.service

[Service]
Type=simple
ExecStart=/usr/local/share/power-bridge/boot-watchdog.sh
Restart=no

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable power-bridge-boot-watchdog.service 2>/dev/null || true
ok "boot-watchdog installed and enabled"

# ── 12. Install power-bridge-update.service ───────────────────────────────────────────
echo -e "\n${GREEN}[12/13]${NC} Installing power-bridge-update.service…"
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

# ── 13. Enable and start services ───────────────────────────────────────────
echo -e "\n${GREEN}[13/13]${NC} Starting services…"
if command -v nmcli >/dev/null 2>&1 && systemctl is-active NetworkManager >/dev/null 2>&1; then
    if ! nmcli -t -f NAME connection show --active | grep -qx "power-bridge-wifi"; then
        nmcli connection up power-bridge-ap 2>/dev/null || warn "power-bridge-ap failed to start"
    fi
else
    systemctl is-active dhcpcd 2>/dev/null && systemctl restart dhcpcd || true
    systemctl restart hostapd 2>/dev/null || warn "hostapd failed to start (may need reboot)"
    systemctl restart dnsmasq 2>/dev/null || warn "dnsmasq failed to start"
fi
systemctl start power-bridge.service 2>/dev/null || warn "power-bridge failed to start"

# ── Done ──────────────────────────────────────────────────────────────────────────────────
IP=$(hostname -I 2>/dev/null | awk '{print $1}' || echo "unknown")

echo ""
echo -e "${GREEN}╔═════════════════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║  power-bridge ${VERSION} installed successfully!             ${NC}"
echo -e "${GREEN}╠════════════════════════════════════════════════════════════════╣${NC}"
echo -e "${GREEN}║  Access Point: ${AP_SSID}                     ${NC}"
echo -e "${GREEN}║  Setup URL:    http://${AP_IP}                         ${NC}"
echo -e "${GREEN}║  Device IP:    http://${IP}                           ${NC}"
echo -e "${GREEN}╠════════════════════════════════════════════════════════════════╣${NC}"
echo -e "${GREEN}║  OTA updates:  automatic at every boot (stable channel)      ${NC}"
echo -e "${GREEN}║  Logs:         journalctl -u power-bridge-update             ${NC}"
echo -e "${GREEN}║  Rollback:     sudo bash $SHARE_DIR/rollback.sh${NC}"
echo -e "${GREEN}╚════════════════════════════════════════════════════════════════╝${NC}"
echo ""
