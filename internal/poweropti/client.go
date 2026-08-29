// Package poweropti implements a polling client for the powerfox poweropti
// local REST API.
//
// The poweropti exposes its readings at:
//
//	GET http://<ip>/value
//
// Authentication uses the HTTP header X-API-KEY. The key is either the
// 12-character device-ID (e.g. "1097bd725557") or the literal string "null",
// depending on the device configuration.
//
// Response (JSON):
//
//	{
//	  "timestamp": 1757053304,
//	  "values": [
//	    { "obis": "1.7.0", "value": 228     },  // W  (>0 consume, <0 feed)
//	    { "obis": "1.8.0", "value": 17784955 }, // Wh consumed (total)
//	    { "obis": "1.8.1", "value": 17784955 }, // Wh consumed (HT)
//	    { "obis": "1.8.2", "value": 0         }, // Wh consumed (NT)
//	    { "obis": "2.8.0", "value": 181       }  // Wh delivered (total)
//	  ]
//	}
package poweropti

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fedzzito/power-bridge/internal/config"
)

// Reading is one snapshot of poweropti measurements.
type Reading struct {
	// Watt is the current net power in watts.
	// Positive  → consuming from grid.
	// Negative  → feeding into grid.
	Watt float64

	// ConsumedWh is the total energy consumed from the grid (Wh) – OBIS 1.8.0.
	ConsumedWh float64

	// HTConsumedWh is the HT (peak-tariff) consumed energy in Wh – OBIS 1.8.1.
	// Zero when the meter does not report HT/NT separately.
	HTConsumedWh float64

	// NTConsumedWh is the NT (off-peak-tariff) consumed energy in Wh – OBIS 1.8.2.
	// Zero when the meter does not report HT/NT separately.
	NTConsumedWh float64

	// DeliveredWh is the total energy fed into the grid (Wh) – OBIS 2.8.0.
	DeliveredWh float64

	// Valid indicates whether the latest poll succeeded.
	Valid bool

	// At is the timestamp of the last successful poll (local clock).
	At time.Time

	// PoweroptiTimestamp is the Unix epoch reported by the device itself (UTC).
	PoweroptiTimestamp int64
}

// obisEntry is one element of the "values" array in the API response.
type obisEntry struct {
	Obis  string  `json:"obis"`
	Value float64 `json:"value"`
}

// apiResponse maps the JSON returned by the poweropti local /value endpoint.
type apiResponse struct {
	Timestamp int64        `json:"timestamp"`
	Values    []obisEntry  `json:"values"`
}

// Client polls the poweropti and exposes the latest Reading.
type Client struct {
	cfg      *config.Config
	mu       sync.RWMutex
	latest   Reading
	errors   int    // consecutive error count
	lastErr  string // last poll error message
	notifyCh chan struct{}
}

// NewClient creates a Client from the given config.
func NewClient(cfg *config.Config) *Client {
	return &Client{
		cfg:      cfg,
		notifyCh: make(chan struct{}, 1),
	}
}

// Notify returns a channel that receives a signal after each successful poll.
// The channel is buffered (size 1); if the consumer is slow, signals are coalesced.
func (c *Client) Notify() <-chan struct{} {
	return c.notifyCh
}

// Run starts the polling loop and blocks until ctx is cancelled. The interval
// is re-read from cfg before every wait so a change saved via the web UI
// (PollIntervalS) takes effect on the next cycle without a restart.
func (c *Client) Run(ctx context.Context) {
	// Poll immediately on start.
	c.poll()

	for {
		interval := time.Duration(c.cfg.PollIntervalS) * time.Second
		if interval < time.Second {
			interval = time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			c.poll()
		}
	}
}

// Latest returns the most recent Reading in a thread-safe manner.
func (c *Client) Latest() Reading {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latest
}

// ConsecutiveErrors returns how many polls have failed in a row.
func (c *Client) ConsecutiveErrors() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.errors
}

// LastError returns the error message from the most recent failed poll,
// or an empty string if the last poll succeeded.
func (c *Client) LastError() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastErr
}

// FetchOnce performs a single HTTP request to the poweropti and returns the
// result. It does not update the client's cached reading.
func (c *Client) FetchOnce() (*Reading, error) {
	return c.fetch()
}

func (c *Client) poll() {
	r, err := c.fetch()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.errors++
		c.lastErr = err.Error()
		stale := time.Duration(c.cfg.StaleTimeoutS) * time.Second
		if time.Since(c.latest.At) > stale {
			c.latest.Valid = false
		}
		log.Printf("poweropti poll error (#%d): %v", c.errors, err)
		return
	}
	c.errors = 0
	c.lastErr = ""
	c.latest = *r
	// Signal listeners (non-blocking, coalesced).
	select {
	case c.notifyCh <- struct{}{}:
	default:
	}
}

func (c *Client) fetch() (*Reading, error) {
	ip := c.cfg.PoweroptiIP
	ip = strings.TrimPrefix(ip, "https://")
	ip = strings.TrimPrefix(ip, "http://")
	ip = strings.TrimRight(ip, "/")
	if ip == "" {
		return nil, fmt.Errorf("poweropti IP not configured")
	}
	url := fmt.Sprintf("http://%s/value", ip)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header["X-API-KEY"] = []string{c.cfg.PoweroptiAPIKey}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("poweropti returned 401 Unauthorized – API key wrong or interface not enabled")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var ar apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if ar.Timestamp == 0 {
		return nil, fmt.Errorf("invalid response: timestamp is zero")
	}

	// Extract values by OBIS code.
	obisMap := make(map[string]float64, len(ar.Values))
	for _, v := range ar.Values {
		obisMap[v.Obis] = v.Value
	}

	// 1.7.0 is the instantaneous power and must be present.
	watt, ok := obisMap["1.7.0"]
	if !ok {
		return nil, fmt.Errorf("response missing OBIS 1.7.0 (instantaneous power)")
	}

	reading := &Reading{
		Watt:               watt,
		ConsumedWh:         obisMap["1.8.0"], // Wh total import
		HTConsumedWh:       obisMap["1.8.1"], // Wh HT import (0 if absent)
		NTConsumedWh:       obisMap["1.8.2"], // Wh NT import (0 if absent)
		DeliveredWh:        obisMap["2.8.0"], // Wh total export
		Valid:              true,
		At:                 time.Now(),
		PoweroptiTimestamp: ar.Timestamp,
	}

	return reading, nil
}
