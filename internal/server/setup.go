package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/fedzzito/power-bridge/internal/config"
)

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
	var devices []discoveredDevice

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
	devices := discoverPoweropti()
	if devices == nil {
		devices = []discoveredDevice{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"devices": devices})
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

// applyWifiConfig writes /etc/wpa_supplicant/wpa_supplicant.conf and triggers
// wpa_supplicant to reconnect. Errors are logged but not fatal.
func applyWifiConfig(ssid, password string) {
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
	// Restart networking; ignore errors on non-Pi systems.
	_ = exec.Command("systemctl", "restart", "wpa_supplicant@wlan0").Run()
	_ = exec.Command("systemctl", "stop", "hostapd").Run()
	_ = exec.Command("systemctl", "stop", "dnsmasq").Run()
	_ = exec.Command("systemctl", "restart", serviceName).Run()
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
