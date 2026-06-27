package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os/exec"
	"time"

	"github.com/fedzzito/power-bridge/internal/config"
)

// --------------------------------------------------------------------------
// Status page  (GET /)
// --------------------------------------------------------------------------

type statusPageData struct {
	Hostname    string
	Configured  bool
	PoweroptiOK bool
	PoweroptiIP string
	WattDisplay string
	Errors      int
	Logs        []string
	SetupDone   bool
	// LastReadAt is the RFC3339 timestamp of the last successful poweropti poll.
	LastReadAt string
	// PoweroptiAge is a human-readable age string (e.g. "3s ago").
	PoweroptiAge string
	// Version is the power-bridge release version (e.g. "v0.1.3").
	Version string
}

func (s *Server) statusPage(w http.ResponseWriter, r *http.Request) {
	data := s.buildStatusPageData(r)
	if err := s.tmplStatus.Execute(w, data); err != nil {
		log.Printf("status template error: %v", err)
	}
}

func (s *Server) buildStatusPageData(r *http.Request) statusPageData {
	data := statusPageData{
		Hostname:    s.cfg.Hostname,
		Configured:  s.cfg.Configured,
		PoweroptiIP: s.cfg.PoweroptiIP,
		Logs:        s.logBuffer.lines(),
		SetupDone:   r.URL.Query().Get("setup") == "done",
		Version:     Version,
	}
	if s.poller != nil {
		rd := s.poller.Latest()
		data.PoweroptiOK = rd.Valid
		data.Errors = s.poller.ConsecutiveErrors()
		if rd.Valid {
			data.WattDisplay = formatWatt(rd.Watt)
		} else {
			data.WattDisplay = "–"
		}
		if !rd.At.IsZero() {
			data.LastReadAt = rd.At.UTC().Format(time.RFC3339)
			data.PoweroptiAge = formatAge(time.Since(rd.At))
		}
	}
	return data
}

func formatWatt(w float64) string {
	if w >= 0 {
		return jsonFloat(w) + " W (Bezug)"
	}
	return jsonFloat(-w) + " W (Einspeisung)"
}

func jsonFloat(f float64) string {
	b, _ := json.Marshal(round3(f))
	return string(b)
}

func formatAge(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return d.String() + " ago"
	}
	if d < time.Hour {
		return d.Round(time.Second).String() + " ago"
	}
	return "> 1h ago"
}

// --------------------------------------------------------------------------
// /api/status  – JSON for live UI updates
// --------------------------------------------------------------------------

type apiStatusResponse struct {
	Configured         bool    `json:"configured"`
	PoweroptiOK        bool    `json:"poweropti_ok"`
	Watt               float64 `json:"watt"`
	ConsumedWh         float64 `json:"consumed_wh"`
	DeliveredWh        float64 `json:"delivered_wh"`
	Errors             int     `json:"errors"`
	LastError          string  `json:"last_error"`
	UptimeSeconds      int64   `json:"uptime_s"`
	LastReadAt         string  `json:"last_read_at"`        // RFC3339 or ""
	PoweroptiTimestamp int64   `json:"poweropti_timestamp"` // device unix epoch

	// Bridge network identity
	BridgeIP          string `json:"bridge_ip"`
	BridgeMAC         string `json:"bridge_mac"`
	WifiSSID          string `json:"wifi_ssid"`           // configured SSID (from saved config)
	WifiConnectedSSID string `json:"wifi_connected_ssid"` // actually associated SSID
	WifiRSSI          int    `json:"wifi_rssi"`
	IsAPMode          bool   `json:"is_ap_mode"`

	// Poweropti identity
	PoweroptiIP  string `json:"poweropti_ip"`
	PoweroptiMAC string `json:"poweropti_mac"` // from ARP cache

	// Shelly emulation identity
	ShellyID    string `json:"shelly_id"`
	ShellyMAC   string `json:"shelly_mac"`
	ShellyModel string `json:"shelly_model"`
	PhaseMode   string `json:"phase_mode"`

	// Software version
	AppVersion string `json:"version"`
}

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	jsonHeader(w)
	bridgeIP := currentBridgeIP()
	resp := apiStatusResponse{
		Configured:    s.cfg.Configured,
		UptimeSeconds: uptimeSeconds(),

		BridgeIP:          bridgeIP,
		BridgeMAC:         getInterfaceMAC("wlan0"),
		WifiSSID:          s.cfg.WIFISSID,
		WifiConnectedSSID: getConnectedSSID(),
		WifiRSSI:          getWifiRSSI(),
		IsAPMode:          isCaptivePortalMode(),

		PoweroptiIP:  s.cfg.PoweroptiIP,
		PoweroptiMAC: lookupMACFromARP(s.cfg.PoweroptiIP),

		ShellyID:    shellyID(s.cfg.ShellyMAC),
		ShellyMAC:   s.cfg.ShellyMAC,
		ShellyModel: "SPEM-003CEBEU",
		PhaseMode:   string(s.cfg.PhaseMode),

		AppVersion: Version,
	}
	if s.poller != nil {
		rd := s.poller.Latest()
		resp.PoweroptiOK = rd.Valid
		resp.Watt = rd.Watt
		resp.ConsumedWh = rd.ConsumedWh
		resp.DeliveredWh = rd.DeliveredWh
		resp.Errors = s.poller.ConsecutiveErrors()
		resp.LastError = s.poller.LastError()
		resp.PoweroptiTimestamp = rd.PoweroptiTimestamp
		if !rd.At.IsZero() {
			resp.LastReadAt = rd.At.UTC().Format(time.RFC3339)
		}
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// --------------------------------------------------------------------------
// /api/logs
// --------------------------------------------------------------------------

func (s *Server) apiLogs(w http.ResponseWriter, r *http.Request) {
	jsonHeader(w)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"lines": s.logBuffer.lines(),
	})
}

// --------------------------------------------------------------------------
// /api/restart
// --------------------------------------------------------------------------

func (s *Server) apiRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	jsonHeader(w)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "restarting"})
	go func() {
		time.Sleep(200 * time.Millisecond)
		// Run reboot via systemd-run so the command executes in a new transient
		// unit outside the power-bridge cgroup.  This prevents systemd from
		// killing the child process (systemctl) when it tears down the service.
		_ = exec.Command("systemd-run", "--no-block", "systemctl", "reboot").Start()
	}()
}

// --------------------------------------------------------------------------
// /api/factory-reset  – wipes user-specific data (WiFi, poweropti) so the
// device can be handed to a new owner in a clean state.
// --------------------------------------------------------------------------

func (s *Server) apiFactoryReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	jsonHeader(w)

	s.cfg.WIFISSID = ""
	s.cfg.WIFIPassword = ""
	s.cfg.PoweroptiIP = ""
	s.cfg.PoweroptiAPIKey = ""
	s.cfg.Configured = false

	if err := config.Save(s.configPath, s.cfg); err != nil {
		log.Printf("factory-reset: save error: %v", err)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	log.Printf("factory-reset: config cleared, restoring AP mode")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "reset"})
	go func() {
		time.Sleep(300 * time.Millisecond)
		// Remove the home-WiFi NM profile so it does not compete with AP mode
		// after restart. Without this the Pi ends up in a state where NM keeps
		// trying the old WiFi while power-bridge (now unconfigured) never calls
		// enableAPMode(), leaving the device unreachable.
		if hasNmcli() {
			_ = exec.Command("nmcli", "connection", "delete", "power-bridge-wifi").Run()
		}
		enableAPMode()
		time.Sleep(500 * time.Millisecond)
		_ = exec.Command("systemd-run", "--no-block",
			"systemctl", "restart", serviceName).Start()
	}()
}

// --------------------------------------------------------------------------
// /api/test/poweropti  – ad-hoc test against the poweropti endpoint
// --------------------------------------------------------------------------

func (s *Server) apiTestPoweropti(w http.ResponseWriter, r *http.Request) {
	jsonHeader(w)
	if s.poller == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"valid": false, "error": "not configured"})
		return
	}
	rd, err := s.poller.FetchOnce()
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"valid": false, "error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"valid":               true,
		"watt":                rd.Watt,
		"consumed_wh":         rd.ConsumedWh,
		"delivered_wh":        rd.DeliveredWh,
		"poweropti_timestamp": rd.PoweroptiTimestamp,
	})
}
