package server

import (
	"net"
	"net/http"
	"strings"
)

func (s *Server) captivePortalMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isCaptivePortalMode() {
			next.ServeHTTP(w, r)
			return
		}

		host := normalizeHost(r.Host)
		path := r.URL.Path

		if r.Method == http.MethodGet {
			switch path {
			case "/generate_204", "/gen_204":
				// Return 204 even in AP mode: a 302 here causes Android to detect a
				// captive portal and immediately switch back to home WiFi when home
				// WiFi is in range.  With 204 Android stays connected and treats the
				// network as having internet; the general redirect middleware below
				// catches all real browser traffic and sends it to /wifi.
				s.logf("captive: rule=android-probe-204 host=%s path=%s", host, path)
				w.WriteHeader(http.StatusNoContent)
				return
			case "/hotspot-detect.html", "/library/test/success.html":
				s.logf("captive: rule=apple-probe-path host=%s path=%s", host, path)
				serveAppleCaptiveLanding(w, r)
				return
			}
			if host == "captive.apple.com" || host == "www.captive.apple.com" {
				s.logf("captive: rule=apple-probe-host host=%s path=%s", host, path)
				serveAppleCaptiveLanding(w, r)
				return
			}
		}

		if path == "/" {
			s.logf("captive: rule=root-redirect host=%s path=%s", host, path)
			redirectToCaptiveWifi(w, r)
			return
		}

		if isForeignHost(host) && !isCaptiveAllowlistedPath(path) {
			s.logf("captive: rule=foreign-host host=%s path=%s", host, path)
			redirectToCaptiveWifi(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func normalizeHost(hostport string) string {
	host := strings.TrimSpace(strings.ToLower(hostport))
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "[") {
		h, _, err := net.SplitHostPort(host)
		if err == nil {
			return strings.Trim(h, "[]")
		}
		return strings.Trim(host, "[]")
	}
	h, _, err := net.SplitHostPort(host)
	if err == nil {
		return h
	}
	return host
}

func isForeignHost(host string) bool {
	switch host {
	case "", apModeIP, "power-bridge", "power-bridge.local", "localhost", "127.0.0.1", "::1":
		return false
	default:
		return true
	}
}

func isCaptiveAllowlistedPath(path string) bool {
	switch path {
	case "/wifi", "/wifi/connect", "/api/status", "/setup/scan", "/setup/scan-poweropti", "/favicon.ico":
		return true
	}
	return false
}

func redirectToCaptiveWifi(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "http://"+apModeIP+"/wifi", http.StatusFound)
}

func serveAppleCaptiveLanding(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "http://"+apModeIP+"/wifi", http.StatusFound)
}
