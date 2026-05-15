package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/fedzzito/power-bridge/internal/config"
)

func hasNmcli() bool {
	_, err := exec.LookPath("nmcli")
	return err == nil
}

// --------------------------------------------------------------------------
// Poweropti auto-discovery (GET /setup/scan-poweropti)
//
// Tries to resolve the well-known hostname "poweropti" / "poweropti.local"
// and looks up each found IP in the kernel ARP table to retrieve the MAC
// address, which doubles as the poweropti API key.
// --------------------------------------------------------------------------

// discoveredDevice holds the network identity of a found poweropti unit.
type discoveredDevice struct {
	IP  string `json:"ip"`
	MAC string `json:"mac"` // uppercase, colon-separated – also used as API key
}

// discoverPoweropti resolves the well-known "poweropti" hostname via both
// plain DNS and mDNS (.local) and deduplicates the results.
func discoverPoweropti() []discoveredDevice {
	seen := make(map[string]struct{})
	devices := []discoveredDevice{}

	for _, hostname := range []string{"poweropti", "poweropti.local"} {
		addrs, err := net.LookupHost(hostname)
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if _, ok := seen[addr]; ok {
				continue
			}
			seen[addr] = struct{}{}
			mac := lookupMACFromARP(addr)
			devices = append(devices, discoveredDevice{IP: addr, MAC: mac})
		}
	}
	return devices
}

func (s *Server) setupScanPoweropti(w http.ResponseWriter, r *http.Request) {
	jsonHeader(w)
	_ = json.NewEncoder(w).Encode(map[string]any{"devices": discoverPoweropti()})
}

// --------------------------------------------------------------------------
// Setup page (GET /setup)
// --------------------------------------------------------------------------

type setupPageData struct {
	Error   string
	Success string
	Cfg     *config.Config
}

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	data := setupPageData{Cfg: s.cfg}
	if err := s.tmplSetup.Execute(w, data); err != nil {
		log.Printf("setup template error: %v", err)
	}
}

// --------------------------------------------------------------------------
// Setup save (POST /setup/save)
// --------------------------------------------------------------------------

func (s *Server) setupSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}

	s.cfg.WIFISSID = strings.TrimSpace(r.FormValue("wifi_ssid"))
	s.cfg.WIFIPassword = r.FormValue("wifi_password")
	s.cfg.PoweroptiIP = strings.TrimSpace(r.FormValue("poweropti_ip"))
	s.cfg.PoweroptiAPIKey = strings.TrimSpace(r.FormValue("poweropti_api_key"))
	s.cfg.ShellyMAC = strings.TrimSpace(r.FormValue("shelly_mac"))
	s.cfg.Hostname = strings.TrimSpace(r.FormValue("hostname"))

	if profile := r.FormValue("device_profile"); profile != "" {
		s.cfg.DeviceProfile = config.DeviceProfile(profile)
	}
	if phaseMode := r.FormValue("phase_mode"); phaseMode != "" {
		s.cfg.PhaseMode = config.PhaseDistribution(phaseMode)
	}

	s.cfg.Configured = true

	if err := config.Save(s.configPath, s.cfg); err != nil {
		data := setupPageData{Error: fmt.Sprintf("Fehler beim Speichern: %v", err), Cfg: s.cfg}
		_ = s.tmplSetup.Execute(w, data)
		return
	}

	s.logf("Setup completed. SSID=%s, Poweropti=%s", s.cfg.WIFISSID, s.cfg.PoweroptiIP)

	// Write avahi mDNS service file and wifi config; restart networking (best-effort).
	go func() {
		time.Sleep(500 * time.Millisecond)
		writeAvahiService(s.cfg)
		applyWifiConfig(s.cfg.WIFISSID, s.cfg.WIFIPassword)
	}()

	http.Redirect(w, r, "/?setup=done", http.StatusSeeOther)
}

// applyWifiConfig prefers NetworkManager via nmcli and falls back to the old
// wpa_supplicant flow when nmcli is unavailable.
func applyWifiConfig(ssid, password string) {
	if hasNmcli() {
		_ = exec.Command("nmcli", "connection", "delete", "power-bridge-ap").Run()
		_ = exec.Command("nmcli", "connection", "delete", "power-bridge-wifi").Run()

		var addErr error
		if strings.TrimSpace(password) == "" {
			addErr = exec.Command(
				"nmcli", "connection", "add", "type", "wifi", "ifname", "wlan0",
				"con-name", "power-bridge-wifi", "ssid", ssid,
			).Run()
		} else {
			addErr = exec.Command(
				"nmcli", "connection", "add", "type", "wifi", "ifname", "wlan0",
				"con-name", "power-bridge-wifi", "ssid", ssid,
				"wifi-sec.key-mgmt", "wpa-psk", "wifi-sec.psk", password,
			).Run()
		}
		if addErr != nil {
			log.Printf("nmcli add wifi connection failed: %v", addErr)
			return
		}
		if err := exec.Command("nmcli", "connection", "up", "power-bridge-wifi").Run(); err != nil {
			log.Printf("nmcli up power-bridge-wifi failed: %v", err)
		}

		checkScript := fmt.Sprintf(`#!/bin/sh
sleep %d
CONN_STATE=$(nmcli -g GENERAL.STATE connection show power-bridge-wifi 2>/dev/null || true)
DEV_STATE=$(nmcli -g GENERAL.STATE device show wlan0 2>/dev/null || true)
DEV_STATE_CODE=$(echo "$DEV_STATE" | awk '{print $1}')
if [ "$CONN_STATE" != "activated" ] && [ "$DEV_STATE_CODE" != "100" ]; then
    logger -t power-bridge "WiFi connection timed out after %ds – reverting to AP mode"
    nmcli connection delete power-bridge-wifi 2>/dev/null || true
    nmcli connection show power-bridge-ap >/dev/null 2>&1 || \
        nmcli connection add type wifi ifname wlan0 con-name power-bridge-ap ssid "%s" \
            802-11-wireless.mode ap 802-11-wireless.band bg \
            ipv4.method shared ipv4.addresses %s/24
    nmcli connection up power-bridge-ap 2>/dev/null || true
else
    logger -t power-bridge "WiFi connected via NetworkManager"
fi
systemctl restart power-bridge
`, wifiConnectionTimeoutSecs, wifiConnectionTimeoutSecs, apSSID, apModeIP)
		scriptPath := "/etc/power-bridge/wifi-check.sh"
		if err := os.WriteFile(scriptPath, []byte(checkScript), 0o700); err != nil {
			log.Printf("wifi-check script write failed: %v – falling back to direct restart", err)
			_ = exec.Command("systemctl", "restart", serviceName).Run()
			return
		}
		cmd := exec.Command("setsid", "/bin/sh", scriptPath)
		if err := cmd.Start(); err != nil {
			log.Printf("wifi-check script launch failed: %v – falling back to direct restart", err)
			_ = exec.Command("systemctl", "restart", serviceName).Run()
		}
		return
	}

	wpaConf := fmt.Sprintf(`country=DE
ctrl_interface=DIR=/var/run/wpa_supplicant GROUP=netdev
update_config=1

network={
	ssid="%s"
	psk="%s"
	key_mgmt=WPA-PSK
}
`, ssid, password)

	if err := writeFileRoot("/etc/wpa_supplicant/wpa_supplicant.conf", wpaConf); err != nil {
		log.Printf("wpa_supplicant write failed: %v", err)
		return
	}
	// Stop any running wpa_supplicant and AP services cleanly, then start fresh
	// with the new config. Using stop + start (not restart) ensures the daemon
	// actually re-reads the new credentials rather than keeping a stale state.
	_ = exec.Command("systemctl", "stop", "wpa_supplicant@wlan0").Run()
	_ = exec.Command("systemctl", "stop", "wpa_supplicant").Run()
	_ = exec.Command("systemctl", "stop", "hostapd").Run()
	_ = exec.Command("systemctl", "stop", "dnsmasq").Run()
	time.Sleep(500 * time.Millisecond)
	_ = exec.Command("systemctl", "start", "wpa_supplicant@wlan0").Run()

	// Write and launch a detached check script that restarts power-bridge after
	// confirming (or giving up on) the WiFi connection.
	checkScript := fmt.Sprintf(`#!/bin/sh
# Wait for wpa_supplicant to associate.
sleep %d
IP=$(ip -4 addr show wlan0 2>/dev/null | grep 'inet ' | awk '{print $2}' | cut -d/ -f1 | head -1)
if [ -z "$IP" ] || [ "$IP" = "192.168.4.1" ]; then
    logger -t power-bridge "WiFi connection timed out after %ds – reverting to AP mode"
    systemctl stop wpa_supplicant@wlan0 2>/dev/null || true
    systemctl stop wpa_supplicant 2>/dev/null || true
    systemctl start hostapd 2>/dev/null || true
    systemctl start dnsmasq 2>/dev/null || true
else
    logger -t power-bridge "WiFi connected: $IP"
fi
systemctl restart power-bridge
`, wifiConnectionTimeoutSecs, wifiConnectionTimeoutSecs)
	scriptPath := "/etc/power-bridge/wifi-check.sh"
	if err := os.WriteFile(scriptPath, []byte(checkScript), 0o755); err != nil {
		log.Printf("wifi-check script write failed: %v – falling back to direct restart", err)
		_ = exec.Command("systemctl", "restart", serviceName).Run()
		return
	}
	cmd := exec.Command("setsid", "/bin/sh", scriptPath)
	if err := cmd.Start(); err != nil {
		log.Printf("wifi-check script launch failed: %v – falling back to direct restart", err)
		_ = exec.Command("systemctl", "restart", serviceName).Run()
	}
}

// apModeRestartDelaySecs is how long to wait after enabling AP mode before
// restarting the service, giving hostapd/dnsmasq time to become active.
const apModeRestartDelaySecs = 3

// wifiConnectionTimeoutSecs is the time in seconds the bridge waits after
// attempting a WiFi connection before deciding it has failed and reverting
// to AP mode.
const wifiConnectionTimeoutSecs = 35

// enableAPMode prefers NetworkManager via nmcli and falls back to
// hostapd/dnsmasq for systems without nmcli.
func enableAPMode() {
	log.Println("Enabling AP mode…")
	if hasNmcli() {
		_ = exec.Command("nmcli", "connection", "delete", "power-bridge-ap").Run()
		if err := exec.Command(
			"nmcli", "connection", "add", "type", "wifi", "ifname", "wlan0",
			"con-name", "power-bridge-ap", "ssid", apSSID,
			"802-11-wireless.mode", "ap",
			"802-11-wireless.band", "bg",
			"ipv4.method", "shared",
			"ipv4.addresses", apModeIP+"/24",
		).Run(); err != nil {
			log.Printf("nmcli add AP connection failed: %v", err)
		}
		if err := exec.Command("nmcli", "connection", "up", "power-bridge-ap").Run(); err != nil {
			log.Printf("nmcli up power-bridge-ap failed: %v", err)
		}
		log.Println("AP mode enabled")
		return
	}
	_ = exec.Command("systemctl", "stop", "wpa_supplicant@wlan0").Run()
	_ = exec.Command("systemctl", "stop", "wpa_supplicant").Run()
	time.Sleep(500 * time.Millisecond)
	_ = exec.Command("systemctl", "start", "hostapd").Run()
	_ = exec.Command("systemctl", "start", "dnsmasq").Run()
	log.Println("AP mode enabled")
}

// nmSystemConnectionsDir is the directory where NetworkManager stores
// persistent connection profiles (including secrets such as psk / wep-key0).
const nmSystemConnectionsDir = "/etc/NetworkManager/system-connections"

// forgetNMWifiConnections removes ALL NetworkManager WiFi station-mode
// connection profiles from wlan0, including their on-disk files, so that no
// credentials (psk, wep-key0, …) survive in any secret store.
//
// AP-mode profiles (802-11-wireless.mode=ap) such as power-bridge-ap are
// intentionally preserved because they are required for the setup hotspot.
func forgetNMWifiConnections() {
	// 1. Delete the well-known profiles we manage by name.
	for _, name := range []string{"power-bridge-wifi", "preconfigured"} {
		_ = exec.Command("nmcli", "connection", "delete", name).Run()
	}

	// 2. Enumerate all remaining NM connections and delete any that are
	//    of type 802-11-wireless (wifi) and NOT in AP mode.
	out, err := exec.Command("nmcli", "-t", "-f", "NAME,TYPE", "connection", "show").Output()
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) != 2 || parts[1] != "802-11-wireless" {
				continue
			}
			name := parts[0]
			if name == "" || name == "power-bridge-ap" {
				continue
			}
			_ = exec.Command("nmcli", "connection", "delete", name).Run()
		}
	}

	// 3. Belt-and-suspenders: remove any leftover .nmconnection files that
	//    represent wifi station-mode profiles. nmcli delete should have handled
	//    this already, but explicit removal ensures that secrets do not persist
	//    even when NM was unable to delete the file (e.g. permission edge-cases).
	entries, err := os.ReadDir(nmSystemConnectionsDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			filePath := nmSystemConnectionsDir + "/" + e.Name()
			data, readErr := os.ReadFile(filePath)
			if readErr != nil {
				continue
			}
			content := string(data)
			// Only touch wifi connections that are NOT in AP mode.
			if strings.Contains(content, "type=wifi") && !strings.Contains(content, "mode=ap") {
				_ = os.Remove(filePath)
			}
		}
	}

	// 4. Reload NM so it drops any in-memory cached secrets.
	_ = exec.Command("nmcli", "connection", "reload").Run()
}

// apiWifiForget clears the stored WiFi credentials and switches to AP mode so
// the user can reconfigure the network via the setup page.
func (s *Server) apiWifiForget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	jsonHeader(w)
	s.cfg.WIFISSID = ""
	s.cfg.WIFIPassword = ""
	if err := config.Save(s.configPath, s.cfg); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": err.Error()})
		return
	}
	if hasNmcli() {
		forgetNMWifiConnections()
	} else {
		// Write an empty wpa_supplicant.conf so the OS cannot reconnect to the old
		// network after the service restarts. The country code DE matches the value
		// used in applyWifiConfig; adjust at install time if deploying outside Germany.
		emptyWpaConf := "country=DE\nctrl_interface=DIR=/var/run/wpa_supplicant GROUP=netdev\nupdate_config=1\n"
		if err := writeFileRoot("/etc/wpa_supplicant/wpa_supplicant.conf", emptyWpaConf); err != nil {
			s.logf("wpa_supplicant clear failed: %v", err)
		}
	}
	s.logf("WiFi credentials cleared – switching to AP mode")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "ap_mode_enabled",
		"ssid":   apSSID,
		"url":    "http://" + apModeIP,
	})
	go func() {
		time.Sleep(200 * time.Millisecond)
		enableAPMode()
		// Restart the service after AP mode is active so the process starts
		// cleanly in setup-mode context.
		time.Sleep(time.Duration(apModeRestartDelaySecs) * time.Second)
		s.logf("Restarting service after AP mode switch")
		_ = exec.Command("systemctl", "restart", serviceName).Run()
	}()
}

// --------------------------------------------------------------------------
// Captive portal detection endpoints
//
// When the bridge is in AP mode (192.168.4.1), all captive portal probes from
// iOS, Android, and Windows are redirected to /setup so the phone automatically
// shows the configuration page.  In normal (client) mode the expected responses
// are returned so devices don't show a "no internet" warning.
// --------------------------------------------------------------------------

func (s *Server) captivePortal204(w http.ResponseWriter, r *http.Request) {
	if isAPMode() {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) captivePortalHotspot(w http.ResponseWriter, r *http.Request) {
	if isAPMode() {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintln(w, "<HTML><HEAD><TITLE>Success</TITLE></HEAD><BODY>\nSuccess\n</BODY></HTML>")
}

func (s *Server) captivePortalNCSI(w http.ResponseWriter, r *http.Request) {
	if isAPMode() {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	fmt.Fprintln(w, "Microsoft NCSI")
}

func (s *Server) captivePortalConnectTest(w http.ResponseWriter, r *http.Request) {
	if isAPMode() {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	fmt.Fprintln(w, "Microsoft Connect Test")
}

func writeFileRoot(path, content string) error {
	cmd := exec.Command("tee", path)
	cmd.Stdin = strings.NewReader(content)
	return cmd.Run()
}

// writeAvahiService writes a device-specific Avahi mDNS service file and
// restarts avahi-daemon so the Pi is immediately discoverable as a Shelly
// Pro 3EM by the Shelly app and compatible battery systems.
//
// The file is installed to /etc/avahi/services/power-bridge.service.
// Errors are logged but never fatal – the service still works without mDNS.
func writeAvahiService(cfg *config.Config) {
	const avahiPath = "/etc/avahi/services/power-bridge.service"

	id := shellyID(cfg.ShellyMAC)
	mac := macNoColons(cfg.ShellyMAC)

	content := fmt.Sprintf(`<?xml version="1.0" standalone='no'?>
<!DOCTYPE service-group SYSTEM "avahi-service.dtd">
<service-group>
  <name>%s</name>

  <!-- HTTP service so browsers/apps can reach the status page -->
  <service>
    <type>_http._tcp</type>
    <port>80</port>
    <txt-record>path=/</txt-record>
  </service>

  <!-- Shelly Gen2 discovery record -->
  <service>
    <type>_shelly._tcp</type>
    <port>80</port>
    <txt-record>gen=2</txt-record>
    <txt-record>app=Pro3EM</txt-record>
    <txt-record>model=SPEM-003CEBEU</txt-record>
    <txt-record>ver=2.2.1</txt-record>
    <txt-record>id=%s</txt-record>
    <txt-record>mac=%s</txt-record>
  </service>
</service-group>
`, id, id, mac)

	if err := writeFileRoot(avahiPath, content); err != nil {
		log.Printf("failed to write avahi service to %s: %v", avahiPath, err)
		return
	}
	if err := exec.Command("systemctl", "restart", "avahi-daemon").Run(); err != nil {
		log.Printf("avahi-daemon restart failed: %v", err)
	}
}

// macNoColons returns the MAC address in uppercase with colons removed
// (e.g. "B8:27:EB:EE:3B:0B" → "B827EBEE3B0B").
func macNoColons(mac string) string {
	return strings.ToUpper(strings.ReplaceAll(mac, ":", ""))
}

// --------------------------------------------------------------------------
// WiFi scan (GET /setup/scan) – returns JSON list of visible SSIDs
// --------------------------------------------------------------------------

func (s *Server) setupScanWifi(w http.ResponseWriter, r *http.Request) {
	jsonHeader(w)
	out, err := exec.Command("iwlist", "wlan0", "scan").Output()
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	var ssids []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ESSID:") {
			ssid := strings.Trim(strings.TrimPrefix(line, "ESSID:"), `"`)
			if ssid != "" {
				ssids = append(ssids, ssid)
			}
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ssids": ssids})
}
