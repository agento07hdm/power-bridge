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
echo -e "\n${GREEN}[1/9]${NC} Fetching latest release version…"
VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
[ -n "$VERSION" ] || error "Could not determine latest release version from GitHub API"
ok "Latest version: $VERSION"

# ── 2. Download and install binary ────────────────────────────────────────────
echo -e "\n${GREEN}[2/9]${NC} Downloading binary power-bridge-${VERSION}-linux-armv6…"
BINARY_URL="https://github.com/${REPO}/releases/download/${VERSION}/power-bridge-${VERSION}-linux-armv6"
curl -fsSL "$BINARY_URL" -o "$BINARY_DEST"
chmod 755 "$BINARY_DEST"
ok "Binary installed to $BINARY_DEST"

# ── 3. System packages ────────────────────────────────────────────────────────
echo -e "\n${GREEN}[3/9]${NC} Installing required packages…"
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
echo -e "\n${GREEN}[4/9]${NC} Setting up config directory…"
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
echo -e "\n${GREEN}[5/9]${NC} Installing systemd service…"
cat > /etc/systemd/system/power-bridge.service << 'EOF'
[Unit]
Description=power-bridge – powerfox poweropti → virtual Shelly Pro 3EM
Documentation=https://github.com/fedzzito/power-bridge
After=network-online.target
Wants=network-online.target

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
echo -e "\n${GREEN}[6/9]${NC} Registering mDNS service with Avahi…"
mkdir -p "$AVAHI_SVC_DIR"
cat > "$AVAHI_SVC_DIR/power-bridge.service" << 'EOF'
<?xml version="1.0" standalone='no'?>
<!DOCTYPE service-group SYSTEM "avahi-service.dtd">
<service-group>
  <name replace-wildcards="yes">power-bridge on %h</name>
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
  </service>
</service-group>
EOF
systemctl enable avahi-daemon
systemctl restart avahi-daemon 2>/dev/null || true
ok "Avahi mDNS service registered"

# ── 7. hostapd + dnsmasq (AP mode, only if not already configured) ───────────
echo -e "\n${GREEN}[7/9]${NC} Configuring Access Point (hostapd + dnsmasq)…"

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
echo -e "\n${GREEN}[8/9]${NC} Installing prepare-image.sh…"
mkdir -p "$SHARE_DIR"
cat > "$SHARE_DIR/prepare-image.sh" << 'PREPARE_EOF'
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
# Use a first-boot regeneration script via rc.local if available
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
PREPARE_EOF
chmod 755 "$SHARE_DIR/prepare-image.sh"
ok "prepare-image.sh installed to $SHARE_DIR/prepare-image.sh"

# ── 9. Enable and start services ─────────────────────────────────────────────
echo -e "\n${GREEN}[9/9]${NC} Starting services…"
systemctl restart dhcpcd 2>/dev/null || true
systemctl restart hostapd 2>/dev/null || warn "hostapd failed to start (may need reboot)"
systemctl restart dnsmasq 2>/dev/null || warn "dnsmasq failed to start"
systemctl start power-bridge.service 2>/dev/null || warn "power-bridge failed to start"

# ── Done ──────────────────────────────────────────────────────────────────────
IP=$(hostname -I 2>/dev/null | awk '{print $1}' || echo "unknown")

echo ""
echo -e "${GREEN}╔══════════════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║  power-bridge ${VERSION} installed successfully!${NC}"
echo -e "${GREEN}╠══════════════════════════════════════════════════════════════╣${NC}"
echo -e "${GREEN}║  Access Point: ${AP_SSID}                     ${NC}"
echo -e "${GREEN}║  Setup URL:    http://${AP_IP}                         ${NC}"
echo -e "${GREEN}║  Device IP:    http://${IP}                           ${NC}"
echo -e "${GREEN}╚══════════════════════════════════════════════════════════════╝${NC}"
