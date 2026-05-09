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
		"EM.GetStatus",
		"EMData.GetStatus",
		"Sys.GetStatus",
		"Sys.GetConfig",
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

	for _, field := range []string{
		"id",
		"total_act_energy", "total_act_ret_energy",
		"a_total_act_energy", "a_total_act_ret_energy",
		"b_total_act_energy", "b_total_act_ret_energy",
		"c_total_act_energy", "c_total_act_ret_energy",
	} {
		if _, ok := resp[field]; !ok {
			t.Errorf("missing required field %q", field)
		}
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
