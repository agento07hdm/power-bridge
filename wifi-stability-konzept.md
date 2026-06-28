# WiFi / AP-Mode Stabilitätsanalyse – power-bridge

## 1. Identifizierte Probleme

### 1.1 Race Condition: AP wird gelöscht bevor WiFi bestätigt ist (Hauptproblem)

In `applyWifiConfig()` (NM-Pfad, `setup.go`) läuft die Transition so ab:

```
nmcli connection delete power-bridge-ap    ← AP sofort weg
nmcli connection delete power-bridge-wifi
nmcli connection add ... power-bridge-wifi
nmcli connection up power-bridge-wifi      ← kann scheitern
... (35 Sekunden warten)                   ← Pi hat KEIN Netz
wifi-check.sh: AP neu erstellen falls nötig
```

Der Pi hat für bis zu 35 Sekunden gar keine Netzwerkverbindung. Fällt in diesem
Fenster die Stromversorgung aus oder startet der Pi neu (z. B. weil der Nutzer
denkt, nichts passiert), landet er in folgendem Zustand beim Booten:

- `config.yaml` enthält SSID + Passwort (→ WiFi-Profil vorhanden)  
- `power-bridge-ap` NM-Profil ist **nicht mehr vorhanden** (wurde gelöscht)
- WiFi-Profil verbindet sich nicht (falsches Passwort, Netz nicht in Reichweite)
- **Ergebnis: kein AP, kein WiFi → undefinierter Zustand**

Dies ist die wahrscheinliche Ursache des beschriebenen Problems.

### 1.2 Kein AP-Autostart beim Booten wenn WiFi fehlschlägt

Der `power-bridge`-Dienst hat keine Logik, die beim Start prüft:
„Gibt es eine aktive Verbindung? Falls nicht → AP-Mode aktivieren."

NetworkManager versucht automatisch, bekannte Profile zu verbinden. Wenn das
WiFi-Profil vorhanden, aber das Netz unerreichbar ist (SSID geändert, Router
getauscht), versucht NM wiederholt zu verbinden – aber kein AP springt ein.

### 1.3 `isAPMode()` erkennt den Übergangszustand nicht

```go
func isAPMode() bool {
    return getLocalIP() == apModeIP  // "192.168.4.1"
}
```

`getLocalIP()` gibt die erste nicht-loopback IPv4-Adresse zurück. Während der
35-sekündigen Transitionsphase hat `wlan0` möglicherweise noch die alte
AP-IP, oder gar keine IP. Die Funktion ist kein zuverlässiger Zustandsindikator.

### 1.4 Fehlerhafter NM-Check-Skript-Vergleich

Im generierten `wifi-check.sh` (NM-Pfad):

```sh
DEV_STATE=$(nmcli -g GENERAL.STATE device show wlan0 2>/dev/null || true)
DEV_STATE_CODE=$(echo "$DEV_STATE" | awk '{print $1}')
if [ "$CONN_STATE" != "activated" ] && [ "$DEV_STATE_CODE" != "100" ]; then
```

`nmcli -g GENERAL.STATE device show wlan0` gibt z. B. `"100 (connected)"` aus.
`awk '{print $1}'` liefert `"100"` – das funktioniert. Aber der eigentliche
Fehler: `nmcli -g GENERAL.STATE connection show power-bridge-wifi` gibt den
Verbindungsstatus zurück, nicht den Gerätestatus. Wenn das Profil in NM noch als
`"activating"` gilt (nicht `"activated"`), wird die AP-Fallback-Logik ausgelöst,
obwohl die Verbindung wenige Sekunden später erfolgreich wäre.

### 1.5 Kein persistenter Netzwerk-Fallback-Watchdog

Wenn nach erfolgreicher WLAN-Einrichtung das Heimnetz wegfällt (Router-Neustart,
SSID-Änderung), hat der Pi keine Möglichkeit, nach X Minuten ohne Verbindung
selbstständig in den AP-Modus zu wechseln.

### 1.6 Kein physischer Reset-Mechanismus

Ist der Pi im undefiniertem Zustand, kann der Nutzer ohne Netzwerkzugang und
ohne weitere Hardware (Monitor, Tastatur) den Pi nicht zurücksetzen.

---

## 2. Lösungskonzept

### 2.1 AP-Profil niemals löschen, solange WiFi nicht bestätigt ist

**Änderung in `applyWifiConfig()` (NM-Pfad):**

Statt das AP-Profil sofort zu löschen, nur deaktivieren und Autoconnect sperren:

```go
// JETZT: AP sofort löschen (gefährlich)
exec.Command("nmcli", "connection", "delete", "power-bridge-ap").Run()

// BESSER: AP deaktivieren, aber behalten
exec.Command("nmcli", "connection", "modify", "power-bridge-ap",
    "connection.autoconnect", "no").Run()
exec.Command("nmcli", "connection", "down", "power-bridge-ap").Run()
```

Das `wifi-check.sh` aktiviert bei Erfolg `autoconnect no` dauerhaft auf dem
AP-Profil (es bleibt als Fallback) oder löscht es gezielt – erst dann, wenn
WiFi wirklich verbunden ist.

**Ablauf neu:**
```
1. AP-Profil: autoconnect=no, deaktivieren     ← AP noch vorhanden
2. WiFi-Profil erstellen und verbinden
3. 35s warten
4. Verbindung OK?
   → JA: AP-Profil löschen (oder behalten als Backup)
   → NEIN: AP-Profil reaktivieren (nmcli connection up power-bridge-ap)
           WiFi-Profil löschen
```

**Änderung im Fallback-Check-Skript:**
```sh
if [ "$CONN_STATE" != "activated" ] && [ "$DEV_STATE_CODE" != "100" ]; then
    # Fehlgeschlagen: AP wiederherstellen
    nmcli connection delete power-bridge-wifi 2>/dev/null || true
    nmcli connection modify power-bridge-ap connection.autoconnect yes 2>/dev/null || true
    nmcli connection up power-bridge-ap 2>/dev/null || true
else
    # Erfolgreich: AP endgültig entfernen, SSID in config speichern
    nmcli connection delete power-bridge-ap 2>/dev/null || true
fi
```

### 2.2 Boot-Zeit-Fallback: AP erzwingen wenn kein Netz

Ein neuer Startup-Check in `power-bridge` (oder als separates systemd
`ExecStartPre`-Script): Wenn nach dem Boot nach 60 Sekunden keine
Netzwerkverbindung besteht, AP-Mode erzwingen.

**Als Go-Goroutine im Startup (`main.go` oder `server.go`):**

```go
// Nach dem Start 60s warten, dann Verbindung prüfen.
// Kein Netz → AP-Mode aktivieren.
go func() {
    time.Sleep(60 * time.Second)
    if !isConnectedToNetwork() {
        log.Println("No network after 60s – forcing AP mode")
        enableAPMode()
    }
}()
```

```go
func isConnectedToNetwork() bool {
    ip := getLocalIP()
    return ip != apModeIP && ip != "" && !strings.HasPrefix(ip, "169.254")
}
```

Besser als reiner IP-Check: tatsächliche NM-Verbindung prüfen:
```go
out, _ := exec.Command("nmcli", "-g", "GENERAL.STATE",
    "device", "show", "wlan0").Output()
return strings.Contains(string(out), "100")
```

### 2.3 Persistenter Netzwerk-Watchdog (Langzeitstabilität)

Ein Hintergrund-Goroutine im laufenden Betrieb:

```go
func (s *Server) runNetworkWatchdog(ctx context.Context) {
    disconnectedSince := time.Time{}
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            if isConnectedToNetwork() {
                disconnectedSince = time.Time{}
                continue
            }
            if disconnectedSince.IsZero() {
                disconnectedSince = time.Now()
                continue
            }
            if time.Since(disconnectedSince) > 10*time.Minute {
                s.logf("Network lost for >10min – forcing AP mode")
                enableAPMode()
                disconnectedSince = time.Time{}
            }
        }
    }
}
```

Dieser Watchdog fängt den Fall ab, dass das Heimnetz nach erfolgreicher
Einrichtung dauerhaft wegfällt.

### 2.4 Mehrfaches Stecker-Ziehen als Reset-Mechanismus

**Konzept:** Der Pi zählt, wie oft er kurz nach dem Boot wieder abgeschaltet
wurde. Nach N schnellen Neustarts (z. B. 3×) erzwingt er den AP-Mode und löscht
die WLAN-Zugangsdaten.

**Implementierung (als systemd-Unit + Shell-Script oder in Go):**

Beim Start: Datei `/etc/power-bridge/boot-counter` lesen/schreiben.

```sh
# /usr/local/share/power-bridge/boot-watchdog.sh
# Wird als ExecStartPost in power-bridge.service aufgerufen
COUNTER_FILE="/etc/power-bridge/boot-counter"
STABLE_MARKER="/run/power-bridge-stable"
MAX_RESETS=3
STABLE_SECS=120   # nach 2 Min stabilem Betrieb: Counter zurücksetzen

COUNT=$(cat "$COUNTER_FILE" 2>/dev/null || echo "0")
COUNT=$((COUNT + 1))
echo "$COUNT" > "$COUNTER_FILE"

if [ "$COUNT" -ge "$MAX_RESETS" ]; then
    logger -t power-bridge "Boot-Counter=$COUNT >= $MAX_RESETS – forcing AP reset"
    echo "0" > "$COUNTER_FILE"
    # WLAN-Config zurücksetzen
    sed -i 's/^wifi_ssid:.*/wifi_ssid: ""/' /etc/power-bridge/config.yaml
    sed -i 's/^wifi_password:.*/wifi_password: ""/' /etc/power-bridge/config.yaml
    sed -i 's/^configured:.*/configured: false/' /etc/power-bridge/config.yaml
    # AP aktivieren (NM)
    nmcli connection delete power-bridge-wifi 2>/dev/null || true
    nmcli connection show power-bridge-ap >/dev/null 2>&1 || \
        nmcli connection add type wifi ifname wlan0 con-name power-bridge-ap \
            ssid "power-bridge" 802-11-wireless.mode ap \
            802-11-wireless.band bg ipv4.method shared \
            ipv4.addresses 192.168.4.1/24
    nmcli connection up power-bridge-ap 2>/dev/null || true
fi

# Nach STABLE_SECS: Counter zurücksetzen
sleep "$STABLE_SECS"
echo "0" > "$COUNTER_FILE"
```

**Einbindung in `power-bridge.service`:**

```ini
[Service]
ExecStartPost=/bin/bash /usr/local/share/power-bridge/boot-watchdog.sh &
```

Oder als eigenständige systemd-Unit `power-bridge-boot-watchdog.service`:

```ini
[Unit]
Description=power-bridge boot-counter reset watchdog
After=power-bridge.service
BindsTo=power-bridge.service

[Service]
Type=simple
ExecStart=/bin/bash /usr/local/share/power-bridge/boot-watchdog.sh
Restart=no
```

**Nutzerführung:** Im Web-UI und in der README dokumentieren:
> „Wenn der Pi nicht erreichbar ist: Stecker 3× ziehen und wieder einstecken
> (je ~5 Sekunden warten). Der Pi sendet dann wieder den Einrichtungs-WLAN
> `power-bridge` aus."

**Zeitfenster-Empfehlung:**
- Boot-Counter-Reset nach **120 Sekunden** stabilem Betrieb
- 3 Neustarts innerhalb von 120s = Reset
- Nutzer zieht also 3× den Stecker, wartet jeweils ~5s

### 2.5 `isAPMode()` robuster machen

Statt ausschließlich auf IP zu prüfen, NM-Zustand oder eine Statusdatei nutzen:

```go
func isAPMode() bool {
    // Primär: NM-Profil-Zustand
    if hasNmcli() {
        out, err := exec.Command("nmcli", "-g", "GENERAL.STATE",
            "connection", "show", "power-bridge-ap").Output()
        if err == nil && strings.Contains(string(out), "activated") {
            return true
        }
    }
    // Fallback: IP-Vergleich
    return getLocalIP() == apModeIP
}
```

Alternativ: Eine Statusdatei `/run/power-bridge-mode` schreiben (`ap` oder
`wifi`) bei jedem Moduswechsel – schnell lesbar ohne Prozessaufruf.

---

## 3. Reihenfolge der Umsetzung (Priorität)

| # | Änderung | Aufwand | Wirkung |
|---|----------|---------|---------|
| 1 | AP-Profil nicht löschen vor WiFi-Bestätigung | klein | hoch – behebt Hauptproblem |
| 2 | Boot-Counter / Stecker-Ziehen-Reset | mittel | hoch – Nutzer-Fallback |
| 3 | Startup-Fallback: AP nach 60s ohne Netz | klein | mittel |
| 4 | Netzwerk-Watchdog (10min-Timeout) | mittel | mittel |
| 5 | `isAPMode()` robuster | klein | niedrig |

---

## 4. Zusammenfassung der Kernänderungen in setup.go

```go
// applyWifiConfig – NM-Pfad, überarbeitete Version
func applyWifiConfig(ssid, password string) {
    if hasNmcli() {
        // ── Schritt 1: AP sichern, nicht löschen ──────────────────────────
        _ = exec.Command("nmcli", "connection", "modify",
            "power-bridge-ap", "connection.autoconnect", "no").Run()
        _ = exec.Command("nmcli", "connection", "down", "power-bridge-ap").Run()

        // ── Schritt 2: Altes WiFi-Profil entfernen, neues anlegen ─────────
        _ = exec.Command("nmcli", "connection", "delete", "power-bridge-wifi").Run()
        // ... add + up wie bisher ...

        // ── Schritt 3: Check-Skript mit korrekter Fallback-Logik ──────────
        checkScript := fmt.Sprintf(`#!/bin/sh
sleep %d
CONN=$(nmcli -g GENERAL.STATE connection show power-bridge-wifi 2>/dev/null || echo "")
if [ "$CONN" = "activated" ]; then
    logger -t power-bridge "WiFi connected – removing AP profile"
    nmcli connection delete power-bridge-ap 2>/dev/null || true
else
    logger -t power-bridge "WiFi failed – restoring AP mode"
    nmcli connection delete power-bridge-wifi 2>/dev/null || true
    nmcli connection modify power-bridge-ap connection.autoconnect yes 2>/dev/null || true
    nmcli connection up power-bridge-ap 2>/dev/null || true
fi
systemctl restart power-bridge
`, wifiConnectionTimeoutSecs)
        // ... scriptPath schreiben + setsid wie bisher ...
    }
}
```

---

## 5. config.yaml – empfohlenes neues Feld

```yaml
# Vom System gesetzt: "ap" | "wifi" | "unknown"
# Wird beim Boot-Watchdog und isAPMode() genutzt.
network_mode: "ap"
```

Dies erlaubt dem Dienst beim Start sofort den letzten bekannten Modus zu kennen,
ohne auf IP-Erkennung oder NM-Abfragen angewiesen zu sein.
