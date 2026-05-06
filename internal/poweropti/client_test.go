package poweropti_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fedzzito/power-bridge/internal/config"
	"github.com/fedzzito/power-bridge/internal/poweropti"
)

// mockPoweropti starts a fake poweropti HTTP server that returns the given watts
// using the local REST API format (GET /value, X-API-KEY header).
func mockPoweropti(t *testing.T, watts float64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/value" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-API-KEY") != "testapikey" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"timestamp": 1757053304,
			"values": []map[string]any{
				{"obis": "1.7.0", "value": watts},
				{"obis": "1.8.0", "value": 17784955.0}, // Wh consumed
				{"obis": "1.8.1", "value": 17784955.0},
				{"obis": "1.8.2", "value": 0.0},
				{"obis": "2.8.0", "value": 181.0}, // Wh delivered
			},
		})
	}))
}

func TestClientPollsAndUpdatesReading(t *testing.T) {
	srv := mockPoweropti(t, 1500.0)
	defer srv.Close()

	cfg := config.Defaults()
	cfg.PoweroptiIP = srv.Listener.Addr().String()
	cfg.PoweroptiAPIKey = "testapikey"
	cfg.PollIntervalS = 1
	cfg.StaleTimeoutS = 5

	client := poweropti.NewClient(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go client.Run(ctx)

	// Wait for at least one successful poll.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rd := client.Latest()
		if rd.Valid && rd.Watt == 1500.0 {
			// Energy counters are returned in Wh directly per spec
			if rd.ConsumedWh != 17784955.0 {
				t.Errorf("ConsumedWh: want 17784955, got %v", rd.ConsumedWh)
			}
			if rd.DeliveredWh != 181.0 {
				t.Errorf("DeliveredWh: want 181, got %v", rd.DeliveredWh)
			}
			if rd.PoweroptiTimestamp != 1757053304 {
				t.Errorf("PoweroptiTimestamp: want 1757053304, got %v", rd.PoweroptiTimestamp)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("no valid reading received within timeout; last: %+v", client.Latest())
}

func TestClientFeedInNegative(t *testing.T) {
	srv := mockPoweropti(t, -800.0) // negative = feeding into grid
	defer srv.Close()

	cfg := config.Defaults()
	cfg.PoweroptiIP = srv.Listener.Addr().String()
	cfg.PoweroptiAPIKey = "testapikey"
	cfg.PollIntervalS = 1
	cfg.StaleTimeoutS = 5

	client := poweropti.NewClient(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go client.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rd := client.Latest()
		if rd.Valid && rd.Watt == -800.0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("expected watt=-800, got %+v", client.Latest())
}

func TestClientUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.PoweroptiIP = srv.Listener.Addr().String()
	cfg.PoweroptiAPIKey = "wrongkey"
	cfg.PollIntervalS = 1
	cfg.StaleTimeoutS = 2

	client := poweropti.NewClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	client.Run(ctx)

	rd := client.Latest()
	if rd.Valid {
		t.Error("reading should not be valid after 401 response")
	}
	if client.ConsecutiveErrors() == 0 {
		t.Error("expected at least one consecutive error after 401")
	}
}

func TestClientLatestThreadSafe(t *testing.T) {
	cfg := config.Defaults()
	cfg.PoweroptiIP = "127.0.0.1:1"
	cfg.PoweroptiAPIKey = "key"
	cfg.PollIntervalS = 60
	cfg.StaleTimeoutS = 10

	client := poweropti.NewClient(cfg)

	// Concurrent reads should not race
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			_ = client.Latest()
			done <- struct{}{}
		}()
	}
	timeout := time.After(2 * time.Second)
	for i := 0; i < 10; i++ {
		select {
		case <-done:
		case <-timeout:
			t.Fatal("timed out waiting for goroutines")
		}
	}
}

func TestClientFreshHasNoValidReading(t *testing.T) {
	cfg := config.Defaults()
	client := poweropti.NewClient(cfg)
	rd := client.Latest()
	if rd.Valid {
		t.Error("fresh client should not have valid reading")
	}
	if !rd.At.IsZero() {
		t.Error("fresh client At should be zero time")
	}
}
