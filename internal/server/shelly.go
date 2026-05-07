package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/fedzzito/power-bridge/internal/config"
)

// shellyID returns the Shelly device identifier derived from the MAC address.
func shellyID(mac string) string {
	clean := strings.ReplaceAll(strings.ToLower(mac), ":", "")
	return "shellypro3em-" + clean
}

// --------------------------------------------------------------------------
// Shelly.GetDeviceInfo
// --------------------------------------------------------------------------

type deviceInfoResponse struct {
	Name       string  `json:"name"`
	ID         string  `json:"id"`
	MAC        string  `json:"mac"`
	Model      string  `json:"model"`
	Gen        int     `json:"gen"`
	FwID       string  `json:"fw_id"`
	Ver        string  `json:"ver"`
	App        string  `json:"app"`
	AuthEn     bool    `json:"auth_en"`
	AuthDomain *string `json:"auth_domain"`
}

func (s *Server) shellyGetDeviceInfo(w http.ResponseWriter, r *http.Request) {
	jsonHeader(w)
	resp := deviceInfoResponse{
		Name:       s.cfg.Hostname,
		ID:         shellyID(s.cfg.ShellyMAC),
		MAC:        macNoColons(s.cfg.ShellyMAC),
		Model:      "SPEM-003CEBEU",
		Gen:        2,
		FwID:       "20231219-133953/v2.2.1-g21b75e0",
		Ver:        "2.2.1",
		App:        "Pro3EM",
		AuthEn:     false,
		AuthDomain: nil,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// --------------------------------------------------------------------------
// EM.GetStatus
// --------------------------------------------------------------------------

type emPhase struct {
	Current    float64 `json:"current"`
	Voltage    float64 `json:"voltage"`
	ActPower   float64 `json:"act_power"`
	AprtPower  float64 `json:"aprt_power"`
	PF         float64 `json:"pf"`
	Freq       float64 `json:"freq"`
}

type emStatusResponse struct {
	ID            int      `json:"id"`
	APhase        emPhase  `json:"a_phase"`
	BPhase        emPhase  `json:"b_phase"`
	CPhase        emPhase  `json:"c_phase"`
	NCurrentNull  *float64 `json:"n_current"`
	TotalCurrent  float64  `json:"total_current"`
	TotalActPower float64  `json:"total_act_power"`
	TotalAprtPow  float64  `json:"total_aprt_power"`

	// Flat aliases expected by some clients (Marstek, Noah)
	ACurrent   float64 `json:"a_current"`
	AVoltage   float64 `json:"a_voltage"`
	AActPower  float64 `json:"a_act_power"`
	AAprtPower float64 `json:"a_aprt_power"`
	APF        float64 `json:"a_pf"`
	AFreq      float64 `json:"a_freq"`

	BCurrent   float64 `json:"b_current"`
	BVoltage   float64 `json:"b_voltage"`
	BActPower  float64 `json:"b_act_power"`
	BAprtPower float64 `json:"b_aprt_power"`
	BPF        float64 `json:"b_pf"`
	BFreq      float64 `json:"b_freq"`

	CCurrent   float64 `json:"c_current"`
	CVoltage   float64 `json:"c_voltage"`
	CActPower  float64 `json:"c_act_power"`
	CAprtPower float64 `json:"c_aprt_power"`
	CPF        float64 `json:"c_pf"`
	CFreq      float64 `json:"c_freq"`

	// Energy totals (Wh)
	TotalActEnergy   float64 `json:"total_act_energy"`
	TotalActRetEnergy float64 `json:"total_act_ret_energy"`
}

func (s *Server) shellyEMGetStatus(w http.ResponseWriter, r *http.Request) {
	jsonHeader(w)
	resp := s.buildEMStatus()
	_ = json.NewEncoder(w).Encode(resp)
}

const nominalVoltage = 230.0
const nominalFreq = 50.0

// buildEMStatus constructs an emStatusResponse from the latest poweropti reading.
func (s *Server) buildEMStatus() emStatusResponse {
	reading := func() (watt, consumedWh, deliveredWh float64, valid bool) {
		if s.poller == nil {
			return
		}
		rd := s.poller.Latest()
		return rd.Watt, rd.ConsumedWh, rd.DeliveredWh, rd.Valid
	}

	watt, consumedWh, deliveredWh, _ := reading()

	var aP, bP, cP float64
	switch s.cfg.PhaseMode {
	case config.PhaseL1:
		aP = watt
	default: // equal
		aP = watt / 3
		bP = watt / 3
		cP = watt - aP - bP // remainder goes to C
	}

	phaseData := func(p float64) emPhase {
		var current float64
		if nominalVoltage != 0 {
			current = p / nominalVoltage
		}
		if current < 0 {
			current = -current
		}
		return emPhase{
			Current:   round3(current),
			Voltage:   nominalVoltage,
			ActPower:  round3(p),
			AprtPower: round3(absF(p)),
			PF:        pfFromSign(p),
			Freq:      nominalFreq,
		}
	}

	a := phaseData(aP)
	b := phaseData(bP)
	c := phaseData(cP)

	totalCurrent := a.Current + b.Current + c.Current

	return emStatusResponse{
		ID:            0,
		APhase:        a,
		BPhase:        b,
		CPhase:        c,
		NCurrentNull:  nil,
		TotalCurrent:  round3(totalCurrent),
		TotalActPower: round3(watt),
		TotalAprtPow:  round3(absF(watt)),

		// Flat aliases
		ACurrent: a.Current, AVoltage: a.Voltage, AActPower: a.ActPower,
		AAprtPower: a.AprtPower, APF: a.PF, AFreq: a.Freq,
		BCurrent: b.Current, BVoltage: b.Voltage, BActPower: b.ActPower,
		BAprtPower: b.AprtPower, BPF: b.PF, BFreq: b.Freq,
		CCurrent: c.Current, CVoltage: c.Voltage, CActPower: c.ActPower,
		CAprtPower: c.AprtPower, CPF: c.PF, CFreq: c.Freq,

		TotalActEnergy:    round3(consumedWh),
		TotalActRetEnergy: round3(deliveredWh),
	}
}

// --------------------------------------------------------------------------
// Shelly.GetStatus
// --------------------------------------------------------------------------

type shellyStatusResponse struct {
	EM0      emStatusResponse `json:"em:0"`
	Sys      sysStatus        `json:"sys"`
	Wifi     wifiStatus       `json:"wifi"`
}

type sysStatus struct {
	Uptime    int64  `json:"uptime"`
	MemFree   int    `json:"ram_free"`
	MemTotal  int    `json:"ram_size"`
}

type wifiStatus struct {
	SSID   string `json:"ssid"`
	RSSI   int    `json:"rssi"`
	IP     string `json:"sta_ip"`
	Status string `json:"status"`
}

func (s *Server) shellyGetStatus(w http.ResponseWriter, r *http.Request) {
	jsonHeader(w)
	resp := shellyStatusResponse{
		EM0: s.buildEMStatus(),
		Sys: sysStatus{
			Uptime:   uptimeSeconds(),
			MemFree:  48 * 1024 * 1024,
			MemTotal: 64 * 1024 * 1024,
		},
		Wifi: wifiStatus{
			SSID:   s.cfg.WIFISSID,
			RSSI:   -65,
			IP:     getLocalIP(),
			Status: "got ip",
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// --------------------------------------------------------------------------
// Shelly.GetConfig
// --------------------------------------------------------------------------

type shellyConfigResponse struct {
	EM0 emConfig `json:"em:0"`
	Sys sysConfig `json:"sys"`
}

type emConfig struct {
	ID int `json:"id"`
}

type sysConfig struct {
	Device sysDevConfig `json:"device"`
	Sntp   sntpConfig   `json:"sntp"`
}

type sysDevConfig struct {
	Name     string `json:"name"`
	MAC      string `json:"mac"`
	FwUpdate bool   `json:"fw_update_en"`
}

type sntpConfig struct {
	Server string `json:"server"`
}

func (s *Server) shellyGetConfig(w http.ResponseWriter, r *http.Request) {
	jsonHeader(w)
	resp := shellyConfigResponse{
		EM0: emConfig{ID: 0},
		Sys: sysConfig{
			Device: sysDevConfig{
				Name:     s.cfg.Hostname,
				MAC:      macNoColons(s.cfg.ShellyMAC),
				FwUpdate: false,
			},
			Sntp: sntpConfig{Server: "pool.ntp.org"},
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// --------------------------------------------------------------------------
// Shelly.GetComponents
// --------------------------------------------------------------------------

type componentsResponse struct {
	Components []component `json:"components"`
	Total      int         `json:"total"`
}

type component struct {
	Key    string `json:"key"`
	Status any    `json:"status"`
	Config any    `json:"config"`
}

func (s *Server) shellyGetComponents(w http.ResponseWriter, r *http.Request) {
	jsonHeader(w)
	em := s.buildEMStatus()
	resp := componentsResponse{
		Components: []component{
			{
				Key:    "em:0",
				Status: em,
				Config: emConfig{ID: 0},
			},
		},
		Total: 1,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// --------------------------------------------------------------------------
// Sys.GetStatus
// --------------------------------------------------------------------------

type sysGetStatusResponse struct {
	MAC              string         `json:"mac"`
	RestartRequired  bool           `json:"restart_required"`
	Time             string         `json:"time"`
	Unixtime         int64          `json:"unixtime"`
	Uptime           int64          `json:"uptime"`
	RAMSize          int            `json:"ram_size"`
	RAMFree          int            `json:"ram_free"`
	FSSize           int            `json:"fs_size"`
	FSFree           int            `json:"fs_free"`
	CfgRev           int            `json:"cfg_rev"`
	KvsRev           int            `json:"kvs_rev"`
	ScheduleRev      int            `json:"schedule_rev"`
	WebhookRev       int            `json:"webhook_rev"`
	AvailableUpdates map[string]any `json:"available_updates"`
	ResetReason      int            `json:"reset_reason"`
}

func (s *Server) sysGetStatus(w http.ResponseWriter, r *http.Request) {
	jsonHeader(w)
	now := time.Now()
	resp := sysGetStatusResponse{
		MAC:              macNoColons(s.cfg.ShellyMAC),
		RestartRequired:  false,
		Time:             now.Format("15:04"),
		Unixtime:         now.Unix(),
		Uptime:           uptimeSeconds(),
		RAMSize:          64 * 1024 * 1024,
		RAMFree:          48 * 1024 * 1024,
		FSSize:           1048576,
		FSFree:           524288,
		CfgRev:           1,
		KvsRev:           0,
		ScheduleRev:      0,
		WebhookRev:       0,
		AvailableUpdates: map[string]any{},
		ResetReason:      1,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// --------------------------------------------------------------------------
// Sys.GetConfig
// --------------------------------------------------------------------------

type sysGetConfigResponse struct {
	Device   sysGetConfigDevice   `json:"device"`
	Location sysGetConfigLocation `json:"location"`
	Debug    sysGetConfigDebug    `json:"debug"`
	UIData   map[string]any       `json:"ui_data"`
	RPCUDP   sysGetConfigRPCUDP   `json:"rpc_udp"`
	Sntp     sntpConfig           `json:"sntp"`
	CfgRev   int                  `json:"cfg_rev"`
}

type sysGetConfigDevice struct {
	Name    string `json:"name"`
	MAC     string `json:"mac"`
	FwID    string `json:"fw_id"`
	Profile string `json:"profile"`
}

type sysGetConfigLocation struct {
	TZ  string  `json:"tz"`
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type sysGetConfigDebug struct {
	Level     int            `json:"level"`
	FileLevel *int           `json:"file_level"`
	MQTT      map[string]any `json:"mqtt"`
	Websocket map[string]any `json:"websocket"`
	UDP       map[string]any `json:"udp"`
}

type sysGetConfigRPCUDP struct {
	DstAddr    *string `json:"dst_addr"`
	ListenPort *int    `json:"listen_port"`
}

func (s *Server) sysGetConfig(w http.ResponseWriter, r *http.Request) {
	jsonHeader(w)
	resp := sysGetConfigResponse{
		Device: sysGetConfigDevice{
			Name:    s.cfg.Hostname,
			MAC:     macNoColons(s.cfg.ShellyMAC),
			FwID:    "20231219-133953/v2.2.1-g21b75e0",
			Profile: "triphase",
		},
		Location: sysGetConfigLocation{
			TZ:  "Europe/Berlin",
			Lat: 0,
			Lon: 0,
		},
		Debug: sysGetConfigDebug{
			Level:     2,
			FileLevel: nil,
			MQTT:      map[string]any{"enable": false},
			Websocket: map[string]any{"enable": false},
			UDP:       map[string]any{"addr": nil},
		},
		UIData: map[string]any{},
		RPCUDP: sysGetConfigRPCUDP{
			DstAddr:    nil,
			ListenPort: nil,
		},
		Sntp:   sntpConfig{Server: "time.google.com"},
		CfgRev: 1,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

func absF(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func round3(f float64) float64 {
	return float64(int64(f*1000+0.5)) / 1000
}

func pfFromSign(p float64) float64 {
	if p < 0 {
		return -1.0
	}
	return 1.0
}
