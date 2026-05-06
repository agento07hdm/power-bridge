package poweropti

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fedzzito/power-bridge/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFetchPreservesAPIKeyHeaderCasing(t *testing.T) {
	origClient := http.DefaultClient
	http.DefaultClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			apiKey := req.Header["X-API-KEY"]
			if len(apiKey) == 0 || apiKey[0] != "testapikey" {
				t.Fatalf("expected exact X-API-KEY header, got headers: %#v", req.Header)
			}
			if _, hasCanonicalized := req.Header["X-Api-Key"]; hasCanonicalized {
				t.Fatalf("unexpected canonicalized header key X-Api-Key in request headers: %#v", req.Header)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{
					"timestamp": 1757053304,
					"values": [
						{"obis":"1.7.0","value":121},
						{"obis":"1.8.0","value":5988245},
						{"obis":"2.8.0","value":588}
					]
				}`)),
			}, nil
		}),
	}
	defer func() { http.DefaultClient = origClient }()

	cfg := config.Defaults()
	cfg.PoweroptiIP = "127.0.0.1:1234"
	cfg.PoweroptiAPIKey = "testapikey"

	client := NewClient(cfg)
	_, err := client.fetch()
	if err != nil {
		t.Fatalf("fetch returned error: %v", err)
	}
}
