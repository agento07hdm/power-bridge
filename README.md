# power-bridge

**Schlanker Ersatz für uni-meter auf dem Raspberry Pi Zero W v1.1** — ohne Java.

Ein powerfox **poweropti** wird lokal ausgelesen und als virtueller **Shelly Pro 3EM** (Gen 2) im Netzwerk präsentiert, damit Marstek-, Noah/Growatt- und Hoymiles-Speicher Nulleinspeisung umsetzen können.

---

## Architektur

```
┌─────────────┐  HTTP/Basic-Auth  ┌───────────────────────────────┐
│  poweropti  │ ──────────────── ▶ │                               │
└─────────────┘                   │   power-bridge (Go, ARMv6)    │
                                  │                               │
┌─────────────┐  HTTP / mDNS      │  Port 80                      │
│  Marstek /  │ ◀──────────────── │  ├─ /rpc/EM.GetStatus?id=0   │
│  Noah /     │                   │  ├─ /rpc/Shelly.GetStatus     │
│  Hoymiles   │                   │  ├─ /rpc/Shelly.GetDeviceInfo │
└─────────────┘                   │  ├─ /rpc/Shelly.GetConfig     │
                                  │  ├─ /rpc/Shelly.GetComponents │
                                  │  └─ / (Status-UI)             │
                                  └───────────────────────────────┘
```

**Komponenten:**

| Paket | Aufgabe |
|---|---|
| `cmd/power-bridge` | Entry-point, Flag-Parsing, Signal-Handling |
| `internal/config` | YAML-Config laden/speichern |
| `internal/poweropti` | HTTP-Polling des poweropti, Thread-safe Reading |
| `internal/server` | Unified HTTP-Server (Shelly-API + Status-UI + Setup-UI) |

---

## Hardware-Voraussetzungen

- Raspberry Pi Zero W v1.1 (ARMv6 32-bit)
- Raspberry Pi OS Lite 32-bit (Bookworm oder Bullseye)
- SD-Karte ≥ 4 GB
- USB-Netzteil 5 V / 1 A

---

## SD-Karte vorbereiten (Raspberry Pi Imager)

1. **Raspberry Pi Imager** herunterladen: https://www.raspberrypi.com/software/
2. Gerät wählen: **Raspberry Pi Zero W**
3. OS wählen: **Raspberry Pi OS Lite (32-bit)**
4. Speicher wählen: SD-Karte
5. Zahnrad-Symbol → **Erweiterte Optionen**:
   - Hostname: `power-bridge`
   - SSH aktivieren, Benutzer `pi` mit Passwort
   - **Für Erstinstallation / Entwickler** (Pi muss installscript aus dem Internet herunterladen):
     WiFi-SSID und -Passwort des Heimnetzes eintragen → Pi verbindet sich beim ersten Boot
     automatisch. IP-Adresse im Router-DHCP-Client suchen, dann `ssh pi@<PI_IP>`.
   - **Für Kunden-Deployment** (fertige Image-Weitergabe):
     WiFi-Felder leer lassen – power-bridge startet nach der Installation automatisch
     im AP-Modus und leitet den Kunden durch die Erstkonfiguration.
6. Schreiben starten

> **Alternativ** zum Raspberry Pi Imager: das Skript `scripts/firstboot-wifi.sh` schreibt
> die nötigen Konfigurationsdateien auf eine bereits geflashte SD-Karte.
> Zugangsdaten nur lokal eintragen und nie in dieses Repository committen.

---

## Installation

### Auf dem Pi installieren (empfohlen)

```bash
ssh-keygen -R power-bridge.local   # ggf. alten Fingerabdruck löschen
ssh pi@<PI_IP>                      # IP aus Router-DHCP oder: ssh pi@power-bridge.local
curl -fsSL https://raw.githubusercontent.com/fedzzito/power-bridge/main/install.sh | sudo bash
```

Das Skript:
- Ermittelt automatisch die neueste Release-Version von GitHub
- Lädt die fertige ARMv6-Binary herunter (kein Compiler nötig)
- Installiert avahi-daemon, curl (hostapd/dnsmasq nur als Legacy-Fallback ohne nmcli)
- Legt `/etc/power-bridge/config.yaml` an (nur wenn noch keine existiert)
- Richtet den systemd-Service ein (als Heredoc eingebettet, kein git clone nötig)
- Registriert den mDNS-Service bei Avahi
- Startet den Access Point **"power-bridge"** wenn kein Heim-WLAN konfiguriert ist

### Headless WiFi-Setup ohne Pi Imager (Alternative)

Das Hilfsskript `scripts/firstboot-wifi.sh` schreibt die WLAN-Zugangsdaten auf eine
bereits geflashte SD-Karte. Es unterstützt Bookworm (NetworkManager / firstrun.sh) und
Bullseye/Buster (wpa_supplicant.conf).

```bash
# SD-Karte mounten (Linux):
sudo mount /dev/sdb1 /mnt/pi-boot

# WiFi-Konfiguration schreiben (Platzhalter durch echte Werte ersetzen):
sudo bash scripts/firstboot-wifi.sh /mnt/pi-boot "SSID" "Passwort" DE

# SD-Karte sicher auswerfen:
sudo umount /mnt/pi-boot
```

> ⚠️ **Sicherheitshinweis:** Zugangsdaten nur lokal eingeben und niemals in das
> Repository committen.

---

## Image erstellen (für Serienproduktion / Weitergabe)

### Empfohlener Workflow

```bash
# 1. Auf dem Pi – System bereinigen und Image vorbereiten:
sudo bash /usr/local/share/power-bridge/prepare-image.sh

# 2. Pi herunterfahren:
sudo shutdown -h now

# 3. SD-Karte am PC mit USBImager auslesen:
#    https://bztsrc.gitlab.io/usbimager/
#    → Image-Datei erstellen (z.B. power-bridge.img)

# 4. Optional – Image mit PiShrink verkleinern:
sudo pishrink.sh -za power-bridge.img
#    https://github.com/Drewsif/PiShrink
```

Das Skript `prepare-image.sh` führt folgendes aus:
- Stoppt power-bridge und den Update-Service
- Setzt die Konfiguration auf unkonfigurierte Defaults zurück (`configured: false`)
- Löscht SSH-Host-Keys (werden beim nächsten Boot neu generiert)
- Löscht Machine-ID
- Bereinigt apt/pip/npm-Cache
- Bereinigt Log-Dateien und DHCP-Leases
- Stellt sicher, dass die Partition beim ersten Boot automatisch vergrößert wird
- Füllt freien Speicher mit Nullbytes (bessere Kompression des .img-Files)

---

## Automatische OTA-Updates

power-bridge überprüft **bei jedem Boot automatisch**, ob ein neueres GitHub Release verfügbar ist.

### Verhalten beim Boot

```
Boot
 └─ power-bridge-update.service (oneshot)
     ├─ Kein Internet → sofortiger Weiterstart mit aktueller Version
     ├─ Aktuelle Version → sofortiger Weiterstart
     └─ Neues Release gefunden
         ├─ Binary herunterladen
         ├─ Backup der alten Binary erstellen
         ├─ Neue Binary atomar installieren (power-failure-sicher)
         └─ VERSION-Datei aktualisieren
 └─ power-bridge.service startet (immer nach dem Update-Service)
```

### Update-Kanal wechseln

```bash
# Stable (Standard):
echo "stable" | sudo tee /etc/power-bridge/update-channel

# Beta (Pre-Releases):
echo "beta" | sudo tee /etc/power-bridge/update-channel
```

### Rollback nach fehlerhaftem Update

```bash
sudo bash /usr/local/share/power-bridge/rollback.sh
```

Das Rollback-Skript:
- Stoppt power-bridge
- Stellt die vorherige Binary aus dem Backup wieder her
- Aktualisiert die VERSION-Datei
- Startet power-bridge neu

### Update-Logs ansehen

```bash
# Update-Service-Logs:
journalctl -u power-bridge-update

# Letzter Boot:
journalctl -u power-bridge-update -b
```

### Manuelles Update auslösen

```bash
sudo bash /usr/local/share/power-bridge/update.sh
sudo systemctl restart power-bridge
```

---

## Kunden-Ersteinrichtung via AP-Modus

Wenn das Gerät ohne Heim-WLAN geflasht wurde (Kunden-Deployment), startet power-bridge
automatisch als Access Point. Die Erstkonfiguration läuft vollständig über das Smartphone:

1. Mit dem Smartphone/Laptop mit dem WLAN **"power-bridge"** verbinden
   (kein Passwort)
2. Das Smartphone zeigt automatisch die Konfigurationsseite an (Captive Portal).
   Alternativ Browser öffnen: **http://192.168.4.1**
3. Formular ausfüllen:
   - Heim-WLAN SSID + Passwort
   - poweropti IP-Adresse
   - poweropti API-Key (= Seriennummer des Geräts)
   - Geräteprofil (Marstek / Noah / Standard)
   - Virtuelle Shelly-MAC (frei wählbar, muss im Netz eindeutig sein)
   - Hostname (z.B. `shellypro3em-poweropti`)
4. **"Speichern & Verbinden"** klicken
5. Der Pi verbindet sich automatisch mit dem Heimnetz

---

## poweropti API

Die poweropti-Abfrage erfolgt gegen:

```
GET http://<poweropti_ip>/value
X-API-KEY: <api_key>
```

Erwartetes JSON-Format:

```json
{
  "obis": [
    { "measurand": "1-0:1.7.0", "value": 1234.5 },
    { "measurand": "1-0:1.8.0", "value": 12345.678 },
    { "measurand": "1-0:2.8.0", "value": 100.000 }
  ]
}
```

| OBIS-Kennung | Bedeutung |
|---|---|
| `1-0:1.7.0` | Aktuelle Leistung in W (Bezug) |
| `1-0:2.7.0` | Aktuelle Leistung in W (Einspeisung) |
| `1-0:1.8.0` | Gesamtenergie Bezug (kWh) |
| `1-0:2.8.0` | Gesamtenergie Einspeisung (kWh) |

> **Authentifizierung:** Header `X-API-KEY` mit dem 12-stelligen Geräte-ID (z. B. `1097bd725557`)
> oder dem Literalwert `null` (ohne Anführungszeichen) falls kein Schlüssel konfiguriert ist.

Alternativ werden `currentwatt`/`mw` und `obis1_8_0`/`wh_in` etc. im Legacy-Format unterstützt.

---

## Shelly Pro 3EM Gen-2 API

### `GET /rpc/EM.GetStatus?id=0`

```json
{
  "id": 0,
  "total_act_power": 1200.0,
  "total_aprt_power": 1200.0,
  "total_current": 1.739,
  "a_current": 0.580, "a_voltage": 230.0, "a_act_power": 400.0, "a_pf": 1.0, "a_freq": 50.0,
  "b_current": 0.580, "b_voltage": 230.0, "b_act_power": 400.0, "b_pf": 1.0, "b_freq": 50.0,
  "c_current": 0.580, "c_voltage": 230.0, "c_act_power": 400.0, "c_pf": 1.0, "c_freq": 50.0,
  "total_act_energy": 12345678.0,
  "total_act_ret_energy": 100000.0,
  "n_current": null
}
```

> **Vorzeichen:** `total_act_power > 0` = Netzbezug, `< 0` = Einspeisung
> (entspricht dem Shelly Pro 3EM Gen-2 Verhalten)

### `GET /rpc/Shelly.GetDeviceInfo`

```json
{
  "name": "shellypro3em-poweropti",
  "id": "shellypro3em-aabbccddeeff",
  "mac": "AA:BB:CC:DD:EE:FF",
  "model": "SPEM-003CEBEU",
  "gen": 2,
  "fw_id": "20231219-133953/v2.2.1-g21b75e0",
  "ver": "2.2.1",
  "app": "Pro3EM",
  "auth_en": false,
  "auth_domain": null
}
```

### WebSocket RPC (`WS /rpc`)

Shelly Gen-2-kompatibles RPC-Protokoll über WebSocket (wird von der Shelly-App und einigen Home-Automation-Systemen wie Home Assistant genutzt).

**Request-Format:**
```json
{"id": 1, "src": "user_1", "method": "Shelly.GetDeviceInfo", "params": {}}
```

**Response-Format:**
```json
{"id": 1, "src": "shellypro3em-aabbccddeeff", "dst": "user_1", "result": {...}}
```

**Fehler (unbekannte Methode):**
```json
{"id": 1, "src": "shellypro3em-aabbccddeeff", "dst": "user_1", "error": {"code": -105, "message": "method not found: Unknown.Method"}}
```

**Unterstützte Methoden über WebSocket:**

| Methode | Beschreibung |
|---|---|
| `Shelly.GetDeviceInfo` | Geräteinformationen |
| `Shelly.GetStatus` | Gesamtstatus (EM, Sys, WiFi) |
| `Shelly.GetConfig` | Gerätekonfiguration |
| `Shelly.GetComponents` | Komponenten-Liste |
| `EM.GetStatus` | Energiezähler-Status |
| `Sys.GetStatus` | System-Status |
| `Sys.GetConfig` | System-Konfiguration |

---

## Konfiguration (`/etc/power-bridge/config.yaml`)

```yaml
wifi_ssid: "MyHomeWifi"
wifi_password: "supersecret"
poweropti_ip: "192.168.1.100"
poweropti_api_key: "MY_API_KEY"
shelly_mac: "AA:BB:CC:DD:EE:FF"
hostname: "shellypro3em-poweropti"
device_profile: "standard"   # marstek | noah | hoymiles | standard
phase_mode: "equal"          # equal (L1=L2=L3) | l1 (alles auf L1)
poll_interval_sec: 3
stale_timeout_sec: 30
listen_addr: ":80"
configured: true
```

---

## mDNS / Avahi

Der Dienst erscheint im Netz als:
- **HTTP:** `shellypro3em-poweropti.local` (Port 80)
- **Shelly Discovery:** `_shelly._tcp` (wird von Marstek/Noah-Apps gesucht)

Avahi-Service-Datei: `avahi/power-bridge.service` wird automatisch nach
`/etc/avahi/services/power-bridge.service` installiert.

---

## Service-Verwaltung

```bash
# Status prüfen
systemctl status power-bridge
systemctl status power-bridge-update

# Logs ansehen
journalctl -u power-bridge -f
journalctl -u power-bridge-update -b   # Update-Logs letzter Boot

# Neustart
systemctl restart power-bridge

# Nach Config-Änderung
systemctl restart power-bridge

# Rollback auf vorherige Version
sudo bash /usr/local/share/power-bridge/rollback.sh
```

---

## Tests

```bash
PI="shellypro3em-poweropti.local"  # oder IP-Adresse

# Shelly-API testen
curl "http://$PI/rpc/EM.GetStatus?id=0"
curl "http://$PI/rpc/Shelly.GetStatus"
curl "http://$PI/rpc/Shelly.GetDeviceInfo"

# Statusseite
curl "http://$PI/"

# Poweropti-Test
curl "http://$PI/api/test/poweropti"
```

---

## Marstek / Noah / Hoymiles – Geräteerkennung

### Marstek

1. Marstek-App → Einstellungen → Stromzähler hinzufügen
2. Typ: **Shelly Pro 3EM**
3. Automatische Erkennung aktivieren → der Pi sollte erscheinen
   (mDNS `_shelly._tcp`)
4. Alternativ: IP-Adresse manuell eingeben

**Troubleshooting Marstek:**
- App und Pi müssen im selben Subnetz sein
- Avahi-Daemon muss laufen: `systemctl status avahi-daemon`
- mDNS testen: `avahi-browse -t _shelly._tcp` (vom PC)
- `device_profile: marstek` in config.yaml setzen

### Noah / Growatt

1. Noah-App → Energie-Einstellungen → Externen Zähler hinzufügen
2. Typ: **Shelly Pro 3EM**
3. IP-Adresse des Pi eingeben (IP oder `.local`-Name)
4. `phase_mode: l1` kann helfen, wenn Noah nur L1 auswertet

**Troubleshooting Noah:**
- Prüfen ob `total_act_power` korrekt vorzeichenbehaftet ist (negativ = Einspeisung)
- `device_profile: noah` aktivieren
- manche Noah-Firmware-Versionen benötigen Port 80 explizit

### Allgemeine Tipps

| Problem | Lösung |
|---|---|
| Gerät nicht gefunden | `avahi-browse -at` ausführen, Avahi-Daemon prüfen |
| Leistung immer 0 | `curl http://<PI>/api/test/poweropti` – poweropti-Verbindung testen |
| Falsches Vorzeichen | poweropti-Dokumentation prüfen, ggf. Vorzeichen in config negieren |
| mDNS funktioniert nicht | Im gleichen WLAN? Router blockiert mDNS (Multicast)? |

---

## Performance

- RAM-Verbrauch: typisch **< 10 MB** (Go-Binary ohne VM-Overhead)
- CPU-Last Pi Zero W: **< 1 %** bei 3-Sekunden-Intervall
- Binärgröße (stripped): ca. **4 MB** für ARMv6

---

## Entwicklung / lokaler Test

```bash
# Config anlegen
mkdir -p /tmp/pb-test
cp config.yaml.example /tmp/pb-test/config.yaml
# config.yaml anpassen (configured: true, poweropti_ip etc.)

# Starten (kein root nötig auf Port 8080)
go run ./cmd/power-bridge \
    -config /tmp/pb-test/config.yaml \
    -listen :8080

# Testen
curl "http://localhost:8080/rpc/EM.GetStatus?id=0"
curl "http://localhost:8080/rpc/Shelly.GetDeviceInfo"
```

### ARMv6-Binary lokal bauen (optional)

```bash
GOOS=linux GOARCH=arm GOARM=6 CGO_ENABLED=0 \
    go build -ldflags="-s -w -X main.version=dev -X github.com/fedzzito/power-bridge/internal/server.Version=dev" -trimpath \
    -o power-bridge-dev-linux-armv6 ./cmd/power-bridge
```

GitHub Actions baut und veröffentlicht automatisch eine fertige Binary bei jedem `v*.*.*`-Tag.

---

## Lizenz

MIT
