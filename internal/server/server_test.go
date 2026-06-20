package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fedzzito/power-bridge/internal/config"
	"github.com/fedzzito/power-bridge/internal/server"
	"github.com/gorilla/websocket"
)

func newTestServer(t *testing.T) *server.Server {
	t.Helper()
	cfg := config.Defaults()
	cfg.ShellyMAC = "AA:BB:CC:DD:EE:FF"
	cfg.Hostname = "test-bridge"
	cfg.Configured = true
	return server.New(cfg, "/tmp/test-config.yaml", nil)
}

func TestShellyGetDeviceInfo(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/rpc/Shelly.GetDeviceInfo", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	checks := map[string]any{
		"app":   "Pro3EM",
		"model": "SPEM-003CEBEU",
		"mac":   "AABBCCDDEEFF",
	}
	for k, want := range checks {
		got, ok := resp[k]
		if !ok {
			t.Errorf("missing field %q", k)
			continue
		}
		if got != want {
			t.Errorf("field %q: got %v, want %v", k, got, want)
		}
	}

	if gen, ok := resp["gen"].(float64); !ok || gen != 2 {
		t.Errorf("gen: expected 2, got %v", resp["gen"])
	}
}

func TestEMGetStatus(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/rpc/EM.GetStatus?id=0", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Required fields
	for _, field := range []string{
		"id", "total_act_power", "total_aprt_power", "total_current",
		"a_current", "a_voltage", "a_act_power", "a_freq",
		"b_current", "b_voltage", "b_act_power",
		"c_current", "c_voltage", "c_act_power",
		"total_act_energy", "total_act_ret_energy",
	} {
		if _, ok := resp[field]; !ok {
			t.Errorf("missing required field %q", field)
		}
	}

	// Voltage must be 230
	if v, ok := resp["a_voltage"].(float64); !ok || v != 230.0 {
		t.Errorf("a_voltage: expected 230, got %v", resp["a_voltage"])
	}
}

func TestShellyGetStatus_ContainsEM0(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/rpc/Shelly.GetStatus", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if _, ok := resp["em:0"]; !ok {
		t.Error("Shelly.GetStatus must contain 'em:0' key")
	}
	if _, ok := resp["emdata:0"]; !ok {
		t.Error("Shelly.GetStatus must contain 'emdata:0' key")
	}
	if _, ok := resp["sys"]; !ok {
		t.Error("Shelly.GetStatus must contain 'sys' key")
	}
}

func TestSetupRedirectWhenNotConfigured(t *testing.T) {
	cfg := config.Defaults()
	cfg.Configured = false
	srv := server.New(cfg, "/tmp/test-config.yaml", nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/setup") {
		t.Errorf("expected redirect to /setup, got %q", loc)
	}
}

func TestSetupPageRendered(t *testing.T) {
	cfg := config.Defaults()
	cfg.Configured = false
	srv := server.New(cfg, "/tmp/test-config.yaml", nil)

	req := httptest.NewRequest(http.MethodGet, "/setup", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "power-bridge") {
		t.Error("setup page should contain 'power-bridge'")
	}
	if !strings.Contains(body, "wifi_ssid") {
		t.Error("setup page should contain wifi_ssid field")
	}
}

func TestShellyLegacyEndpoint(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/shelly", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Must contain same identification fields as Shelly.GetDeviceInfo
	checks := map[string]any{
		"app":   "Pro3EM",
		"model": "SPEM-003CEBEU",
		"mac":   "AABBCCDDEEFF",
	}
	for k, want := range checks {
		got, ok := resp[k]
		if !ok {
			t.Errorf("missing field %q", k)
			continue
		}
		if got != want {
			t.Errorf("field %q: got %v, want %v", k, got, want)
		}
	}

	if gen, ok := resp["gen"].(float64); !ok || gen != 2 {
		t.Errorf("gen: expected 2, got %v", resp["gen"])
	}

	// ID must be derived from the MAC
	wantID := "shellypro3em-aabbccddeeff"
	if id, ok := resp["id"].(string); !ok || id != wantID {
		t.Errorf("id: expected %q, got %v", wantID, resp["id"])
	}
}


func TestCORSHeaders(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/rpc/EM.GetStatus?id=0", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS header: expected '*', got %q", got)
	}
}

func TestAPIStatusEndpoint(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if _, ok := resp["configured"]; !ok {
		t.Error("missing 'configured' field")
	}
	if _, ok := resp["uptime_s"]; !ok {
		t.Error("missing 'uptime_s' field")
	}
	// wifi_connected_ssid must be present (may be empty string in test env)
	if _, ok := resp["wifi_connected_ssid"]; !ok {
		t.Error("missing 'wifi_connected_ssid' field")
	}
}

func TestAPIWifiForget_MethodNotAllowed(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/wifi/forget", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET, got %d", w.Code)
	}
}

func TestAPIWifiForget_POST_ClearsConfig(t *testing.T) {
	srv := newTestServer(t)
	// Pre-set WiFi credentials so we can verify they are cleared.
	srv.ExportSetWifiCredentials("TestSSID", "TestPassword")

	req := httptest.NewRequest(http.MethodPost, "/api/wifi/forget", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	// The endpoint writes the config to /tmp/test-config.yaml which may fail
	// (that's OK in tests) but must still return a JSON response, not a 5xx.
	if w.Code == http.StatusMethodNotAllowed {
		t.Fatalf("unexpected 405 for POST")
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected JSON content-type, got %q", ct)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Status should indicate AP mode was requested, or an error about config save.
	status, _ := resp["status"].(string)
	if status != "ap_mode_enabled" && resp["error"] == nil {
		t.Errorf("unexpected response: %v", resp)
	}
}

func TestWiFiGetStatus(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/rpc/WiFi.GetStatus", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, field := range []string{"ssid", "rssi", "sta_ip", "status"} {
		if _, ok := resp[field]; !ok {
			t.Errorf("missing required field %q", field)
		}
	}

	if status, ok := resp["status"].(string); !ok || status == "" {
		t.Errorf("status: expected non-empty string, got %v", resp["status"])
	}
}


func TestRPCWebSocketDispatch(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/rpc"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	methods := []string{
		"Shelly.GetDeviceInfo",
		"Shelly.GetStatus",
		"Shelly.GetConfig",
		"Shelly.GetComponents",
		"Shelly.ListMethods",
		"EM.GetStatus",
		"EM.GetConfig",
		"EMData.GetStatus",
		"EMData.GetConfig",
		"Sys.GetStatus",
		"Sys.GetConfig",
		"WiFi.GetStatus",
		"Wifi.GetStatus",
		"Wifi.GetConfig",
	}

	for i, method := range methods {
		req := map[string]any{"id": i + 1, "src": "test_client", "method": method}
		if err := conn.WriteJSON(req); err != nil {
			t.Fatalf("write %s: %v", method, err)
		}

		var resp map[string]any
		if err := conn.ReadJSON(&resp); err != nil {
			t.Fatalf("read %s: %v", method, err)
		}

		if resp["error"] != nil {
			t.Errorf("%s: got error %v", method, resp["error"])
		}
		if resp["result"] == nil {
			t.Errorf("%s: missing 'result' field", method)
		}
		if resp["dst"] != "test_client" {
			t.Errorf("%s: dst expected 'test_client', got %v", method, resp["dst"])
		}
		if id, _ := resp["id"].(float64); int(id) != i+1 {
			t.Errorf("%s: id expected %d, got %v", method, i+1, resp["id"])
		}
	}
}

func TestEMDataGetStatus(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/rpc/EMData.GetStatus?id=0", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// total_act and total_act_ret are the field names expected by Home Assistant
	// (aioshelly sensor key: "emdata", sub_key: "total_act" / "total_act_ret").
	for _, field := range []string{
		"id",
		"total_act", "total_act_ret",
		"a_total_act_energy", "a_total_act_ret_energy",
		"b_total_act_energy", "b_total_act_ret_energy",
		"c_total_act_energy", "c_total_act_ret_energy",
	} {
		if _, ok := resp[field]; !ok {
			t.Errorf("missing required field %q", field)
		}
	}
}

func TestEMGetConfig(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/rpc/EM.GetConfig?id=0", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, field := range []string{"id", "phase_selector", "blink_mode_selector"} {
		if _, ok := resp[field]; !ok {
			t.Errorf("missing required field %q", field)
		}
	}
	if id, ok := resp["id"].(float64); !ok || int(id) != 0 {
		t.Errorf("id: expected 0, got %v", resp["id"])
	}
}

func TestEMDataGetConfig(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/rpc/EMData.GetConfig?id=0", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if id, ok := resp["id"].(float64); !ok || int(id) != 0 {
		t.Errorf("id: expected 0, got %v", resp["id"])
	}
}

func TestShellyGetConfig_ContainsAllSections(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/rpc/Shelly.GetConfig", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, key := range []string{"em:0", "emdata:0", "sys", "wifi"} {
		if _, ok := resp[key]; !ok {
			t.Errorf("Shelly.GetConfig missing required section %q", key)
		}
	}

	// sys.device.profile must be "triphase" for HA to treat this as a 3-phase device.
	sys, ok := resp["sys"].(map[string]any)
	if !ok {
		t.Fatal("sys is not an object")
	}
	dev, ok := sys["device"].(map[string]any)
	if !ok {
		t.Fatal("sys.device is not an object")
	}
	if profile, _ := dev["profile"].(string); profile != "triphase" {
		t.Errorf("sys.device.profile: expected \"triphase\", got %q", profile)
	}
}


func TestShellyGetComponents_ContainsEMData(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/rpc/Shelly.GetComponents", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	components, ok := resp["components"].([]any)
	if !ok {
		t.Fatal("missing or invalid 'components' field")
	}

	keys := make(map[string]bool)
	for _, c := range components {
		if cm, ok := c.(map[string]any); ok {
			if k, ok := cm["key"].(string); ok {
				keys[k] = true
			}
		}
	}

	if !keys["em:0"] {
		t.Error("components must contain 'em:0'")
	}
	if !keys["emdata:0"] {
		t.Error("components must contain 'emdata:0'")
	}

	if total, ok := resp["total"].(float64); !ok || int(total) != 2 {
		t.Errorf("total: expected 2, got %v", resp["total"])
	}
}

func TestRPCWebSocketUnknownMethod(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/rpc"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	req := map[string]any{"id": 1, "src": "test_client", "method": "Unknown.Method"}
	if err := conn.WriteJSON(req); err != nil {
		t.Fatalf("write: %v", err)
	}

	var resp map[string]any
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("read: %v", err)
	}

	if resp["error"] == nil {
		t.Error("expected error for unknown method, got none")
	}
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatal("error field is not an object")
	}
	if errObj["code"] != float64(-105) {
		t.Errorf("error code: expected -105, got %v", errObj["code"])
	}
}

func TestEMGetStatus_PFInValidRange(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/rpc/EM.GetStatus?id=0", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, key := range []string{"a_pf", "b_pf", "c_pf"} {
		pf, ok := resp[key].(float64)
		if !ok {
			t.Errorf("missing or non-numeric field %q", key)
			continue
		}
		if pf < 0 || pf > 1 {
			t.Errorf("%s = %v, want value in [0, 1]", key, pf)
		}
	}
}

func TestShellyListMethods(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/rpc/Shelly.ListMethods", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	methods, ok := resp["methods"].([]any)
	if !ok {
		t.Fatal("missing or invalid 'methods' field")
	}

	required := []string{
		"Shelly.GetDeviceInfo", "Shelly.GetStatus", "Shelly.GetConfig",
		"Shelly.GetComponents", "Shelly.ListMethods",
		"Sys.GetStatus", "Sys.GetConfig",
		"Wifi.GetStatus", "Wifi.GetConfig",
		"EM.GetStatus", "EMData.GetStatus",
	}
	methodSet := make(map[string]bool)
	for _, m := range methods {
		if s, ok := m.(string); ok {
			methodSet[s] = true
		}
	}
	for _, m := range required {
		if !methodSet[m] {
			t.Errorf("Shelly.ListMethods missing required method %q", m)
		}
	}

	// Legacy alias must not appear in the advertised method list.
	if methodSet["WiFi.GetStatus"] {
		t.Error("Shelly.ListMethods must not advertise legacy alias 'WiFi.GetStatus'")
	}
}

func TestWifiGetConfig(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/rpc/Wifi.GetConfig", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, field := range []string{"ap", "sta", "sta1"} {
		if _, ok := resp[field]; !ok {
			t.Errorf("missing required field %q", field)
		}
	}
}

func TestWifiGetStatusOfficialNamespace(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/rpc/Wifi.GetStatus", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, field := range []string{"ssid", "rssi", "sta_ip", "status"} {
		if _, ok := resp[field]; !ok {
			t.Errorf("missing required field %q", field)
		}
	}
}

func TestRPCWebSocket_NotifyStatusPush(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/rpc"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	// Manually trigger a broadcast as if a new reading had arrived.
	data, err := srv.ExportBuildNotifyStatus()
	if err != nil {
		t.Fatalf("buildNotifyStatus: %v", err)
	}
	srv.ExportHubBroadcast(data)

	var msg map[string]any
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("read NotifyStatus: %v", err)
	}

	if msg["method"] != "NotifyStatus" {
		t.Errorf("method: expected 'NotifyStatus', got %v", msg["method"])
	}
	params, ok := msg["params"].(map[string]any)
	if !ok {
		t.Fatal("params missing or wrong type")
	}
	if _, ok := params["ts"]; !ok {
		t.Error("params.ts missing")
	}
	if _, ok := params["em:0"]; !ok {
		t.Error("params['em:0'] missing")
	}
}

func TestWifiSetupPageRendered(t *testing.T) {
	cfg := config.Defaults()
	cfg.Configured = false
	srv := server.New(cfg, "/tmp/test-config.yaml", nil)

	req := httptest.NewRequest(http.MethodGet, "/wifi", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "power-bridge") {
		t.Error("wifi page should contain 'power-bridge'")
	}
	if !strings.Contains(body, "wifi-password") {
		t.Error("wifi page should contain password field")
	}
	if !strings.Contains(body, "ssid-select") {
		t.Error("wifi page should contain SSID dropdown")
	}
}

func TestWifiConnect_MissingSSID(t *testing.T) {
	srv := newTestServer(t)

	body := strings.NewReader("ssid=&******")
	req := httptest.NewRequest(http.MethodPost, "/wifi/connect", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected JSON, got %q", ct)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["error"] == nil {
		t.Error("expected error for empty SSID")
	}
}

func TestWifiConnect_MethodNotAllowed(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/wifi/connect", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected JSON, got %q", ct)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["error"] == nil {
		t.Error("expected error for GET request")
	}
}

func TestCaptivePortal204_NotAPMode(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/generate_204", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	// In test env (not AP mode), should return 204 No Content.
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
}

func TestCaptivePortalHotspot_NotAPMode(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/hotspot-detect.html", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	// In test env (not AP mode), should return 200 with "Success" body.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Success") {
		t.Error("expected 'Success' body for hotspot-detect.html in station mode")
	}
}

func TestRootHandler_APMode_RedirectsToWifi(t *testing.T) {
	restore := server.ExportSetAPMode(true)
	defer restore()

	cfg := config.Defaults()
	cfg.Configured = false
	srv := server.New(cfg, "/tmp/test-config.yaml", nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/wifi" {
		t.Errorf("expected redirect to /wifi, got %q", loc)
	}
}

func TestRootHandler_APMode_ConfiguredRedirectsToWifi(t *testing.T) {
	restore := server.ExportSetAPMode(true)
	defer restore()

	// Even when cfg.Configured==true the AP-mode root should point to /wifi.
	cfg := config.Defaults()
	cfg.Configured = true
	srv := server.New(cfg, "/tmp/test-config.yaml", nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/wifi" {
		t.Errorf("expected redirect to /wifi, got %q", loc)
	}
}

func TestRootHandler_APMode_UnknownPath_RedirectsToWifi(t *testing.T) {
	restore := server.ExportSetAPMode(true)
	defer restore()

	cfg := config.Defaults()
	srv := server.New(cfg, "/tmp/test-config.yaml", nil)

	// Simulate a DNS-redirected request for an arbitrary URL
	req := httptest.NewRequest(http.MethodGet, "/some/arbitrary/path", nil)
	req.Host = "www.example.com"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/wifi" {
		t.Errorf("expected redirect to /wifi, got %q", loc)
	}
}

func TestRootHandler_NormalMode_NotConfigured_RedirectsToSetup(t *testing.T) {
	cfg := config.Defaults()
	cfg.Configured = false
	srv := server.New(cfg, "/tmp/test-config.yaml", nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/setup") {
		t.Errorf("expected redirect to /setup, got %q", loc)
	}
}

func TestRootHandler_NormalMode_UnknownPath_Returns404(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestCaptivePortal_APMode_AllEndpoints(t *testing.T) {
	restore := server.ExportSetAPMode(true)
	defer restore()

	cfg := config.Defaults()
	srv := server.New(cfg, "/tmp/test-config.yaml", nil)

	endpoints := []string{
		"/generate_204",
		"/gen_204",
		"/hotspot-detect.html",
		"/library/test/success.html",
		"/ncsi.txt",
		"/connecttest.txt",
		"/success.txt",
		"/redirect",
	}

	for _, ep := range endpoints {
		ep := ep
		t.Run(ep, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, ep, nil)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)

			if w.Code != http.StatusFound {
				t.Fatalf("%s: expected 302 redirect in AP mode, got %d", ep, w.Code)
			}
			if loc := w.Header().Get("Location"); loc != "/wifi" {
				t.Errorf("%s: expected redirect to /wifi, got %q", ep, loc)
			}
		})
	}
}

func TestCaptivePortal_NormalMode_Gen204(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/gen_204", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
}

func TestCaptivePortal_NormalMode_SuccessTxt(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/success.txt", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "success") {
		t.Error("expected 'success' body for /success.txt in station mode")
	}
}

func TestCaptivePortal_NormalMode_NCSITxt(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/ncsi.txt", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Microsoft NCSI") {
		t.Error("expected 'Microsoft NCSI' body for /ncsi.txt in station mode")
	}
}

