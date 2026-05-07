package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fedzzito/power-bridge/internal/config"
	"github.com/fedzzito/power-bridge/internal/server"
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
