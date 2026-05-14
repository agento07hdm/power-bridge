#!/usr/bin/env bash
# =============================================================================
# scripts/firstboot-wifi.sh
#
# Schreibt WLAN-Zugangsdaten auf eine bereits geflashte Raspberry Pi OS SD-Karte,
# damit der Pi beim ersten Boot automatisch ins Heimnetz einbucht (Headless-Setup).
#
# Unterstützt:
#   - Raspberry Pi OS Bookworm 32-bit (NetworkManager / firstrun.sh-Mechanismus)
#   - Raspberry Pi OS Bullseye/Buster 32-bit (wpa_supplicant.conf, Fallback)
#
# ⚠️  SICHERHEITSHINWEIS
#     Dieses Skript NUR lokal auf deinem PC ausführen.
#     NIEMALS echte Zugangsdaten in dieses Repository committen.
#     Zugangsdaten nur als Laufzeit-Parameter (Argumente) übergeben.
#     Lege eine lokale Kopie außerhalb des Repos an, wenn du die
#     Zugangsdaten fest eintragen möchtest, und stelle sicher, dass
#     diese Datei in .gitignore eingetragen ist.
#
# Verwendung:
#   sudo bash scripts/firstboot-wifi.sh <BOOT_MOUNT> <SSID> <PASSWORT> [LAND]
#
# Argumente:
#   BOOT_MOUNT   Pfad zur gemounteten FAT32-Boot-Partition der SD-Karte
#                (z. B. /mnt/pi-boot oder /Volumes/bootfs auf macOS)
#   SSID         WLAN-Netzwerkname (z. B. "MeinHeimnetz")
#   PASSWORT     WLAN-Passwort
#   LAND         WLAN-Ländercode nach ISO 3166-1, Standard: DE
#
# Beispiel (Linux):
#   sudo mount /dev/sdb1 /mnt/pi-boot
#   sudo bash scripts/firstboot-wifi.sh /mnt/pi-boot "MeinHeimnetz" "GeheimesPasswort" DE
#   sudo umount /mnt/pi-boot
#
# Beispiel (macOS – Boot-Partition erscheint automatisch als Volume):
#   sudo bash scripts/firstboot-wifi.sh /Volumes/bootfs "MeinHeimnetz" "GeheimesPasswort" DE
#
# Danach:
#   1. SD-Karte einlegen, Pi starten
#   2. Pi-IP-Adresse im DHCP-Client-Log des Routers oder per nmap / arp-scan ermitteln:
#        arp -a | grep power-bridge
#   3. SSH-Verbindung herstellen:
#        ssh pi@<PI_IP>
#   4. Installation starten:
#        curl -fsSL https://raw.githubusercontent.com/fedzzito/power-bridge/main/install.sh | sudo bash
#   5. Nach der Installation: WLAN im Web-UI testen (http://<PI_IP>) und über
#      "WLAN vergessen" den AP-Modus aktivieren.
#
# =============================================================================
set -euo pipefail

RED='\033[0;31m'; YELLOW='\033[1;33m'; GREEN='\033[0;32m'; NC='\033[0m'
err()  { echo -e "${RED}✗  $*${NC}" >&2; exit 1; }
warn() { echo -e "${YELLOW}⚠  $*${NC}"; }
ok()   { echo -e "${GREEN}✓  $*${NC}"; }

# ── Argumente prüfen ──────────────────────────────────────────────────────────
[ $# -lt 3 ] && err "Verwendung: sudo bash $0 <BOOT_MOUNT> <SSID> <PASSWORT> [LAND]"

BOOT="$1"
SSID="$2"
PASSWORD="$3"
COUNTRY="${4:-DE}"

[ -d "$BOOT" ] || err "Verzeichnis '$BOOT' existiert nicht oder ist nicht gemountet."
[ -f "$BOOT/cmdline.txt" ] || err "Keine cmdline.txt in '$BOOT' gefunden – kein Pi-OS-Boot-Verzeichnis?"

echo ""
echo "=================================================================="
echo "  power-bridge – Headless WiFi Setup für Raspberry Pi OS"
echo "=================================================================="
echo ""
warn "Zugangsdaten werden NUR lokal auf die SD-Karte geschrieben."
warn "Diese Daten werden NICHT in das Repository übertragen."
echo ""

# ── Bookworm-Erkennung: firstrun.sh / firstboot-Mechanismus ──────────────────
# Bookworm: Pi Imager schreibt firstrun.sh und trägt den firstboot-Init in
# cmdline.txt ein. Wir ergänzen/erstellen firstrun.sh mit den WLAN-Daten.
# Bullseye-Fallback: wpa_supplicant.conf direkt auf die Boot-Partition schreiben
# (wird von Pi OS beim ersten Boot nach /etc/wpa_supplicant/ kopiert).
# ─────────────────────────────────────────────────────────────────────────────

FIRSTBOOT_INIT="/usr/lib/raspberrypi-sys-mods/firstboot"
CMDLINE_FILE="$BOOT/cmdline.txt"
FIRSTRUN_FILE="$BOOT/firstrun.sh"

CMDLINE=$(cat "$CMDLINE_FILE")

# Shell-sichere Darstellung von SSID und Passwort für die Einbettung in firstrun.sh.
# printf '%q' gibt eine shell-escaped Version aus, die auch Sonderzeichen wie
# $, Backticks, Klammern und Semikolons sicher darstellt.
Q_SSID=$(printf '%q' "$SSID")
Q_PASS=$(printf '%q' "$PASSWORD")

# Schreibt wpa_supplicant.conf via printf, ohne Heredoc-Variablenexpansion.
# Das verhindert, dass Sonderzeichen in SSID/Passwort die Dateistruktur brechen.
write_wpa_conf() {
    local dest="$1"
    {
        printf 'country=%s\n' "$COUNTRY"
        printf 'ctrl_interface=DIR=/var/run/wpa_supplicant GROUP=netdev\n'
        printf 'update_config=1\n\n'
        printf 'network={\n'
        printf '    ssid="%s"\n' "${SSID//\"/\\\"}"
        printf '    psk="%s"\n' "${PASSWORD//\"/\\\"}"
        printf '    key_mgmt=WPA-PSK\n'
        printf '}\n'
    } > "$dest"
    chmod 600 "$dest"
}

if echo "$CMDLINE" | grep -q "firstboot\|firstrun"; then
    # Bookworm mit vorhandenem firstboot-Eintrag
    echo "→ Bookworm-Modus erkannt (firstboot bereits in cmdline.txt)"

    if [ -f "$FIRSTRUN_FILE" ]; then
        # Vorhandenes firstrun.sh ergänzen – WiFi-Block am Ende einfügen,
        # aber vor dem abschließenden 'exit 0' (falls vorhanden).
        echo "  Vorhandenes firstrun.sh wird um WLAN-Konfiguration ergänzt…"
        # Entferne evtl. vorhandenen alten WiFi-Block aus einem früheren Lauf.
        # sed -i.bak ist auf macOS und Linux identisch kompatibel.
        sed -i.bak '/# power-bridge-wifi-start/,/# power-bridge-wifi-end/d' "$FIRSTRUN_FILE" 2>/dev/null || true
        rm -f "${FIRSTRUN_FILE}.bak"
        # Entferne das abschließende 'exit 0', damit wir es am Ende neu anfügen.
        sed -i.bak '/^exit 0$/d' "$FIRSTRUN_FILE" 2>/dev/null || true
        rm -f "${FIRSTRUN_FILE}.bak"
    else
        echo "  Neues firstrun.sh wird erstellt…"
        cat > "$FIRSTRUN_FILE" << 'SHEBANG'
#!/bin/bash
set +e
SHEBANG
    fi

    # WiFi-Konfigurationsblock anhängen.
    # Q_SSID und Q_PASS sind mit printf '%q' shell-escaped: Sonderzeichen wie
    # $, Backticks und Klammern werden durch Backslash-Escaping gesichert,
    # sodass sie in der generierten firstrun.sh nicht als Shell-Konstrukte
    # interpretiert werden können.
    cat >> "$FIRSTRUN_FILE" << WIFIBLOCK

# power-bridge-wifi-start
# WLAN-Konfiguration (geschrieben von scripts/firstboot-wifi.sh)
WLAN_SSID=${Q_SSID}
WLAN_PASS=${Q_PASS}
WLAN_COUNTRY=${COUNTRY}
raspi-config nonint do_wifi_country "\$WLAN_COUNTRY" 2>/dev/null || true
if command -v nmcli >/dev/null 2>&1; then
    nmcli radio wifi on
    nmcli connection add type wifi ifname wlan0 con-name preconfigured ssid "\$WLAN_SSID" \\
        wifi-sec.key-mgmt wpa-psk wifi-sec.psk "\$WLAN_PASS" \\
        connection.autoconnect yes 2>/dev/null || true
    nmcli connection up preconfigured 2>/dev/null || true
fi
# wpa_supplicant-Fallback für Systeme ohne NetworkManager
mkdir -p /etc/wpa_supplicant
printf 'country=%s\nctrl_interface=DIR=/var/run/wpa_supplicant GROUP=netdev\nupdate_config=1\n\nnetwork={\n    ssid="%s"\n    psk="%s"\n    key_mgmt=WPA-PSK\n}\n' \\
    "\$WLAN_COUNTRY" "\$WLAN_SSID" "\$WLAN_PASS" > /etc/wpa_supplicant/wpa_supplicant.conf
chmod 600 /etc/wpa_supplicant/wpa_supplicant.conf
# power-bridge-wifi-end
exit 0
WIFIBLOCK

    chmod +x "$FIRSTRUN_FILE"
    ok "firstrun.sh aktualisiert: $FIRSTRUN_FILE"

    # Sicherstellen, dass der firstboot-Init in cmdline.txt eingetragen ist
    if ! echo "$CMDLINE" | grep -q "init=.*firstboot"; then
        sed -i.bak "1s|\$| init=${FIRSTBOOT_INIT}|" "$CMDLINE_FILE"
        rm -f "${CMDLINE_FILE}.bak"
        ok "firstboot-Init zu cmdline.txt hinzugefügt"
    fi

else
    # Kein firstboot in cmdline – Bullseye / älteres Image → wpa_supplicant.conf
    echo "→ Legacy-Modus (kein firstboot in cmdline.txt – Bullseye/Buster)"

    WPA_FILE="$BOOT/wpa_supplicant.conf"
    write_wpa_conf "$WPA_FILE"
    ok "wpa_supplicant.conf geschrieben: $WPA_FILE"
fi

# ── SSH aktivieren (leere 'ssh'-Datei auf der Boot-Partition) ────────────────
touch "$BOOT/ssh"
ok "SSH für ersten Boot aktiviert ($BOOT/ssh)"

# ── Zusammenfassung ───────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}══════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  ✓ Headless WiFi Setup abgeschlossen!${NC}"
echo -e "${GREEN}══════════════════════════════════════════════════════════${NC}"
echo ""
echo "  SSID:    ${SSID}"
echo "  Land:    ${COUNTRY}"
echo ""
echo "Nächste Schritte:"
echo "  1. SD-Karte sicher auswerfen (sudo umount <BOOT_MOUNT>)"
echo "  2. SD-Karte in den Pi Zero W einlegen und starten"
echo "  3. IP-Adresse im Router-DHCP-Client suchen oder:"
echo "       arp -a | grep power-bridge"
echo "  4. SSH-Verbindung:"
echo "       ssh pi@<PI_IP>"
echo "  5. Installation:"
echo "       curl -fsSL https://raw.githubusercontent.com/fedzzito/power-bridge/main/install.sh | sudo bash"
echo "  6. Nach der Installation unter http://<PI_IP> einloggen."
echo "     Über 'WLAN vergessen' den AP-Modus aktivieren und testen."
echo ""
warn "Tipp: SSID und Passwort niemals in dieses Repository committen!"
echo ""
