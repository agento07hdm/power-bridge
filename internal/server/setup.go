package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
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
	IP        string `json:"ip"`
	MAC       string `json:"mac"`       // uppercase, colon-separated – also used as API key
	Reachable bool   `json:"reachable"` // true if the device answered an ICMP ping
}

// pingIP sends a single ICMP ping to ip to ensure an ARP entry is created,
// and reports whether the host answered.
func pingIP(ip string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "ping", "-c", "1", "-W", "1", ip).Run() == nil
}

// discoverPoweropti resolves the well-known "poweropti" hostname via both
// plain DNS and mDNS (.local), pings each found IP to populate the ARP cache,
// and then reads the MAC address from the kernel ARP table.
// The MAC doubles as the poweropti API key (12 hex chars, no colons).
func discoverPoweropti() []discoveredDevice {
	seen := make(map[string]struct{})
	var ips []string

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
			ips = append(ips, addr)
		}
	}

	// Ping all found IPs concurrently so ARP entries are populated before lookup,
	// and record which ones actually answered.
	reachable := make(map[string]bool, len(ips))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, ip := range ips {
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			ok := pingIP(ip)
			mu.Lock()
			reachable[ip] = ok
			mu.Unlock()
		}(ip)
	}
	wg.Wait()

	devices := make([]discoveredDevice, 0, len(ips))
	for _, addr := range ips {
		mac := lookupMACFromARP(addr)
		devices = append(devices, discoveredDevice{IP: addr, MAC: mac, Reachable: reachable[addr]})
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
	// Setup is now integrated into the main status page.
	http.Redirect(w, r, "/", http.StatusFound)
}

// --------------------------------------------------------------------------
// Dedicated WiFi setup page (GET /wifi)
// Shown via captive portal when the bridge is in AP mode.
// --------------------------------------------------------------------------

func (s *Server) wifiSetupPage(w http.ResponseWriter, r *http.Request) {
	if err := s.tmplWifi.Execute(w, nil); err != nil {
		log.Printf("wifi template error: %v", err)
	}
}

// --------------------------------------------------------------------------
// WiFi connect (POST /wifi/connect)
// Saves only the WiFi credentials and kicks off the connection sequence.
// Returns JSON so the page can poll for status.
// --------------------------------------------------------------------------

func (s *Server) wifiConnect(w http.ResponseWriter, r *http.Request) {
	jsonHeader(w)
	if r.Method != http.MethodPost {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "POST only"})
		return
	}
	if err := r.ParseForm(); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad form data"})
		return
	}

	ssid := strings.TrimSpace(r.FormValue("ssid"))
	if ssid == "" {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "SSID darf nicht leer sein"})
		return
	}

	s.cfg.WIFISSID = ssid
	s.cfg.WIFIPassword = r.FormValue("password")

	if err := config.Save(s.configPath, s.cfg); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("Fehler beim Speichern: %v", err)})
		return
	}

	s.logf("WiFi setup via /wifi: connecting to SSID=%s", ssid)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "connecting"})

	go func() {
		time.Sleep(300 * time.Millisecond)
		applyWifiConfig(s.cfg.WIFISSID, s.cfg.WIFIPassword)
	}()
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
		// Deactivate the AP profile but keep it on disk as a safety net.
		// If WiFi fails to connect the check script restores it; if it succeeds
		// the script removes it. This eliminates the previous race condition
		// where the Pi had no network for up to wifiConnectionTimeoutSecs seconds.
		_ = exec.Command("nmcli", "connection", "modify",
			"power-bridge-ap", "connection.autoconnect", "no").Run()
		_ = exec.Command("nmcli", "connection", "down", "power-bridge-ap").Run()

		// (Re)create the WiFi station profile.
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
			log.Printf("nmcli add wifi connection failed: %v – restoring AP mode", addErr)
			_ = exec.Command("nmcli", "connection", "modify",
				"power-bridge-ap", "connection.autoconnect", "yes").Run()
			_ = exec.Command("nmcli", "connection", "up", "power-bridge-ap").Run()
			return
		}
		if err := exec.Command("nmcli", "connection", "up", "power-bridge-wifi").Run(); err != nil {
			log.Printf("nmcli up power-bridge-wifi failed (check script will decide): %v", err)
		}

		// Detached check script: waits wifiConnectionTimeoutSecs, then decides.
		// Success criteria: NM reports "activated" OR wlan0 has a routable IP.
		// On failure: delete WiFi profile, restore AP fallback profile.
		checkScript := fmt.Sprintf(`#!/bin/sh
sleep %d
CONN=$(nmcli -g GENERAL.STATE connection show power-bridge-wifi 2>/dev/null || echo "")
IP=$(ip -4 addr show wlan0 2>/dev/null | grep 'inet ' | awk '{print $2}' | cut -d/ -f1 | head -1)
if [ "$CONN" = "activated" ] || { [ -n "$IP" ] && [ "$IP" != "%s" ] && [ "${IP#169.254}" = "$IP" ]; }; then
    logger -t power-bridge "WiFi connected (conn=$CONN ip=$IP) – removing AP fallback profile"
    nmcli connection delete power-bridge-ap 2>/dev/null || true
else
    logger -t power-bridge "WiFi failed after %ds (conn=$CONN ip=$IP) – restoring AP mode"
    nmcli connection delete power-bridge-wifi 2>/dev/null || true
    nmcli connection modify power-bridge-ap connection.autoconnect yes 2>/dev/null || \
        nmcli connection add type wifi ifname wlan0 con-name power-bridge-ap ssid "%s" \
            802-11-wireless.mode ap 802-11-wireless.band bg \
            ipv4.method shared ipv4.addresses %s/24 2>/dev/null || true
    nmcli connection up power-bridge-ap 2>/dev/null || true
fi
systemctl restart power-bridge
`, wifiConnectionTimeoutSecs, apModeIP, wifiConnectionTimeoutSecs, apSSID, apModeIP)
		scriptPath := "/etc/power-bridge/wifi-check.sh"
		if err := os.WriteFile(scriptPath, []byte(checkScript), 0o700); err != nil {
			log.Printf("wifi-check script write failed: %v – restoring AP mode and restarting", err)
			_ = exec.Command("nmcli", "connection", "modify",
				"power-bridge-ap", "connection.autoconnect", "yes").Run()
			_ = exec.Command("nmcli", "connection", "up", "power-bridge-ap").Run()
			_ = exec.Command("systemctl", "restart", serviceName).Run()
			return
		}
		cmd := exec.Command("setsid", "/bin/sh", scriptPath)
		if err := cmd.Start(); err != nil {
			log.Printf("wifi-check script launch failed: %v – restoring AP mode and restarting", err)
			_ = exec.Command("nmcli", "connection", "modify",
				"power-bridge-ap", "connection.autoconnect", "yes").Run()
			_ = exec.Command("nmcli", "connection", "up", "power-bridge-ap").Run()
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

// nmDnsmasqSharedDir is the directory where NetworkManager's internal dnsmasq
// reads extra config snippets for connections using ipv4.method=shared.
const nmDnsmasqSharedDir = "/etc/NetworkManager/dnsmasq-shared.d"

// nmCatchallConf is the filename written inside nmDnsmasqSharedDir.
const nmCatchallConf = "power-bridge-catchall.conf"

// standaloneDnsmasqDir is the drop-in directory for the system-wide dnsmasq
// package. Used as a fallback when NM's shared-mode dnsmasq is unavailable.
const standaloneDnsmasqDir = "/etc/dnsmasq.d"

// apDNSConf is the dnsmasq snippet that forces captive-portal detection
// probes and every other DNS query from AP clients to resolve to the AP IP.
func apDNSConf() string {
	return "# power-bridge: captive portal DNS redirect\n" +
		"address=/connectivitycheck.gstatic.com/" + apModeIP + "\n" +
		"address=/clients3.google.com/" + apModeIP + "\n" +
		"address=/#/" + apModeIP + "\n" +
		// RFC 8910: tell DHCP clients directly where the captive portal is.
		// Supported by Android 11+ and iOS 14+.
		"dhcp-option=114,http://" + apModeIP + "/wifi\n"
}

// writeAPDNSConf writes the catch-all dnsmasq snippet to dir/power-bridge-catchall.conf.
// It is idempotent: writing the same content is a no-op, and the directory is
// created if it does not exist.  It returns true on success.
func writeAPDNSConf(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("AP DNS: mkdir %s failed: %v", dir, err)
		return false
	}
	path := dir + "/" + nmCatchallConf
	conf := apDNSConf()

	// Idempotent: skip write when the file already contains the correct content.
	if existing, err := os.ReadFile(path); err == nil && string(existing) == conf {
		log.Printf("AP DNS: %s already up-to-date", path)
		return true
	}

	if err := os.WriteFile(path, []byte(conf), 0o644); err != nil {
		log.Printf("AP DNS: write %s failed: %v", path, err)
		return false
	}
	log.Printf("AP DNS: wrote %s", path)
	return true
}

// enableAPMode prefers NetworkManager via nmcli and falls back to
// hostapd/dnsmasq for systems without nmcli.
func enableAPMode() {
	log.Println("Enabling AP mode…")
	if hasNmcli() {
		// Write the dnsmasq snippet for NM's shared-mode dnsmasq so that
		// captive-portal probes (connectivitycheck.gstatic.com, clients3.google.com)
		// and all other DNS queries from AP clients resolve to the AP IP.
		writeAPDNSConf(nmDnsmasqSharedDir)
		writeAPDNSConf(standaloneDnsmasqDir)
		reloadDnsmasq()
		applyAPIPTables()

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

		// Reload NetworkManager so its internal dnsmasq picks up the new config.
		// nmcli connection up already causes NM to restart its dnsmasq for the
		// shared connection, but an explicit reload guards against edge cases
		// where the connection was already active before this call.
		if out, err := exec.Command("systemctl", "reload-or-restart", "NetworkManager").CombinedOutput(); err != nil {
			log.Printf("NM reload failed (%v): %s", err, strings.TrimSpace(string(out)))
		} else {
			log.Printf("NetworkManager reloaded – dnsmasq path: %s/%s", nmDnsmasqSharedDir, nmCatchallConf)
		}

		// Verify the config file is present after the write attempt.
		if _, err := os.Stat(nmDnsmasqSharedDir + "/" + nmCatchallConf); err == nil {
			log.Printf("AP DNS config verified: %s/%s", nmDnsmasqSharedDir, nmCatchallConf)
		} else {
			log.Printf("AP DNS config NOT found at %s/%s – captive portal may not work", nmDnsmasqSharedDir, nmCatchallConf)
		}

		log.Println("AP mode enabled")
		return
	}

	// Legacy path: hostapd + standalone dnsmasq (no NetworkManager).
	writeAPDNSConf(standaloneDnsmasqDir)
	_ = exec.Command("systemctl", "stop", "wpa_supplicant@wlan0").Run()
	_ = exec.Command("systemctl", "stop", "wpa_supplicant").Run()
	time.Sleep(500 * time.Millisecond)
	_ = exec.Command("systemctl", "start", "hostapd").Run()
	if out, err := exec.Command("systemctl", "restart", "dnsmasq").CombinedOutput(); err != nil {
		log.Printf("dnsmasq restart failed (%v): %s", err, strings.TrimSpace(string(out)))
	} else {
		log.Printf("dnsmasq restarted – config: %s/%s", standaloneDnsmasqDir, nmCatchallConf)
	}
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
	if isCaptivePortalMode() {
		redirectToCaptiveWifi(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) captivePortalHotspot(w http.ResponseWriter, r *http.Request) {
	if isCaptivePortalMode() {
		serveAppleCaptiveLanding(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintln(w, "<HTML><HEAD><TITLE>Success</TITLE></HEAD><BODY>\nSuccess\n</BODY></HTML>")
}

func (s *Server) captivePortalNCSI(w http.ResponseWriter, r *http.Request) {
	if isCaptivePortalMode() {
		redirectToCaptiveWifi(w, r)
		return
	}
	fmt.Fprintln(w, "Microsoft NCSI")
}

func (s *Server) captivePortalConnectTest(w http.ResponseWriter, r *http.Request) {
	if isCaptivePortalMode() {
		redirectToCaptiveWifi(w, r)
		return
	}
	fmt.Fprintln(w, "Microsoft Connect Test")
}

func (s *Server) captivePortalSuccess(w http.ResponseWriter, r *http.Request) {
	if isCaptivePortalMode() {
		redirectToCaptiveWifi(w, r)
		return
	}
	fmt.Fprintln(w, "success")
}

func (s *Server) captivePortalRedirect(w http.ResponseWriter, r *http.Request) {
	if isCaptivePortalMode() {
		redirectToCaptiveWifi(w, r)
		return
	}
	http.Redirect(w, r, "http://www.msftconnecttest.com/redirect", http.StatusFound)
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

	// Set the system hostname to "power-bridge" so that the device is
	// reachable as power-bridge.local via mDNS (avahi uses the system hostname).
	setSystemHostname(apSSID)

	if err := exec.Command("systemctl", "restart", "avahi-daemon").Run(); err != nil {
		log.Printf("avahi-daemon restart failed: %v", err)
	}
}

// setSystemHostname sets the system hostname so the device is reachable
// via mDNS as <name>.local. It uses hostnamectl when available and falls
// back to writing /etc/hostname directly.
func setSystemHostname(name string) {
	if err := exec.Command("hostnamectl", "set-hostname", name).Run(); err != nil {
		// Fallback: write /etc/hostname directly.
		if werr := os.WriteFile("/etc/hostname", []byte(name+"\n"), 0o644); werr != nil {
			log.Printf("setSystemHostname: hostnamectl failed (%v) and /etc/hostname write failed (%v)", err, werr)
		}
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
	_ = json.NewEncoder(w).Encode(map[string]any{"ssids": scanWifiSSIDs()})
}

// scanWifiSSIDs returns a sorted, deduplicated list of visible SSIDs.
// It prefers nmcli (NetworkManager) and falls back to iwlist.
// The device's own AP SSID is filtered out.
func scanWifiSSIDs() []string {
	var ssids []string
	if hasNmcli() {
		out, err := exec.Command("nmcli", "--rescan", "yes", "-g", "SSID", "dev", "wifi", "list").Output()
		if err == nil {
			ssids = parseSSIDLines(strings.Split(string(out), "\n"))
		}
	}
	if len(ssids) == 0 {
		out, err := exec.Command("iwlist", "wlan0", "scan").Output()
		if err == nil {
			var raw []string
			for _, line := range strings.Split(string(out), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "ESSID:") {
					raw = append(raw, strings.Trim(strings.TrimPrefix(line, "ESSID:"), `"`))
				}
			}
			ssids = parseSSIDLines(raw)
		}
	}
	return ssids
}

// parseSSIDLines deduplicates, filters blank/AP entries, and sorts SSIDs.
func parseSSIDLines(lines []string) []string {
	seen := make(map[string]struct{})
	result := []string{}
	for _, s := range lines {
		s = strings.TrimSpace(s)
		if s == "" || s == "--" || s == apSSID {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		result = append(result, s)
	}
	sort.Strings(result)
	return result
}

// reloadDnsmasq sends SIGHUP to all running dnsmasq processes so they
// re-read conf files from dnsmasq-shared.d / dnsmasq.d without restarting.
// This is safe to call even when dnsmasq is not installed; pkill exits non-zero
// in that case but we ignore the error.
func reloadDnsmasq() {
	if out, err := exec.Command("pkill", "-HUP", "dnsmasq").CombinedOutput(); err != nil {
		log.Printf("AP DNS: dnsmasq HUP: %v (%s)", err, strings.TrimSpace(string(out)))
	} else {
		log.Printf("AP DNS: sent SIGHUP to dnsmasq – catch-all config reloaded")
	}
}

// ensureAPDNSOnStartup writes the captive-portal DNS catch-all config and
// signals dnsmasq to reload when the service starts up already in AP mode.
// Call once at startup; it is a no-op when not in AP mode.
// EnsureAPModeOnStartup ensures the device is reachable at startup.
// If wlan0 has no active connection (disconnected) and no home WiFi SSID is
// configured, or if wlan0 is already the AP IP, enableAPMode() is called so
// the setup hotspot is always available after a reboot on a fresh device.
func EnsureAPModeOnStartup(wifiSSID string) {
	wlan0IP := getWlan0IP()
	alreadyAP := isAPMode()

	// Already broadcasting the AP – just ensure DNS config is fresh.
	if alreadyAP || wlan0IP == apModeIP {
		log.Println("startup: AP mode active – ensuring DNS catch-all config and iptables rules")
		writeAPDNSConf(nmDnsmasqSharedDir)
		writeAPDNSConf(standaloneDnsmasqDir)
		time.Sleep(2 * time.Second)
		reloadDnsmasq()
		applyAPIPTables()
		return
	}

	// wlan0 has no IP and no home SSID configured → fresh/reset device.
	if wlan0IP == "" && wifiSSID == "" {
		log.Println("startup: wlan0 disconnected and no SSID configured – enabling AP mode")
		enableAPMode()
		return
	}

	// wlan0 has no IP but a home SSID is configured → NM is probably still
	// associating; the startup-network-fallback watchdog handles this case.
	if wlan0IP == "" {
		log.Printf("startup: wlan0 disconnected (SSID=%q configured) – watchdog will handle fallback", wifiSSID)
	}
}
