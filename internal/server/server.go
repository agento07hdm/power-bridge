// Package server implements the unified HTTP server that provides:
//   - Shelly Pro 3EM Gen-2 API emulation  (/rpc/*)
//   - Status web UI                        (/)
//   - First-run setup web UI               (/setup/*)
package server

import (
	"context"
	"embed"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/fedzzito/power-bridge/internal/config"
	"github.com/fedzzito/power-bridge/internal/poweropti"
)

//go:embed templates/*
var templateFS embed.FS

// Version is the release version string, set via -ldflags "-X github.com/fedzzito/power-bridge/internal/server.Version=vX.Y.Z"
// from the main package. Falls back to "dev" if not set.
var Version = "dev"

// Server is the unified HTTP server.
type Server struct {
	cfg        *config.Config
	configPath string
	poller     *poweropti.Client
	tmplSetup  *template.Template
	tmplStatus *template.Template
	tmplWifi   *template.Template
	httpSrv    *http.Server
	mux        *http.ServeMux
	handler    http.Handler
	logBuffer  *ringLog
	hub        *wsHub
}

// New creates a Server. poller may be nil if the device is not yet configured.
func New(cfg *config.Config, configPath string, poller *poweropti.Client) *Server {
	s := &Server{
		cfg:        cfg,
		configPath: configPath,
		poller:     poller,
		logBuffer:  newRingLog(200),
		hub:        newWSHub(),
	}

	// Parse embedded templates.
	s.tmplSetup = template.Must(
		template.ParseFS(templateFS, "templates/setup.html"),
	)
	s.tmplStatus = template.Must(
		template.ParseFS(templateFS, "templates/status.html"),
	)
	s.tmplWifi = template.Must(
		template.ParseFS(templateFS, "templates/wifi.html"),
	)

	mux := http.NewServeMux()
	s.mux = mux
	s.registerRoutes(mux)
	s.handler = s.captivePortalMiddleware(mux)

	s.httpSrv = &http.Server{
		Handler:      s.handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	return s
}

// ServeHTTP implements http.Handler, enabling use with httptest.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// Listen binds to addr and serves requests; it blocks until the server is shut down.
func (s *Server) Listen(addr string) error {
	s.httpSrv.Addr = addr
	return s.httpSrv.ListenAndServe()
}

// ListenOnPort starts an additional HTTP listener on addr using the same
// request handler as the primary server. It blocks until the listener fails.
// Intended to be called in a separate goroutine.
func (s *Server) ListenOnPort(addr string) error {
	extra := &http.Server{
		Addr:         addr,
		Handler:      s.handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	return extra.ListenAndServe()
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) {
	if err := s.httpSrv.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

// registerRoutes wires all HTTP handlers.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Shelly Pro 3EM Gen-2 RPC API
	mux.HandleFunc("/rpc/Shelly.GetDeviceInfo", s.shellyGetDeviceInfo)
	mux.HandleFunc("/rpc/Shelly.GetStatus", s.shellyGetStatus)
	mux.HandleFunc("/rpc/Shelly.GetConfig", s.shellyGetConfig)
	mux.HandleFunc("/rpc/Shelly.GetComponents", s.shellyGetComponents)
	mux.HandleFunc("/rpc/EM.GetStatus", s.shellyEMGetStatus)
	mux.HandleFunc("/rpc/EM.GetConfig", s.emGetConfig)
	mux.HandleFunc("/rpc/EMData.GetStatus", s.shellyEMDataGetStatus)
	mux.HandleFunc("/rpc/EMData.GetConfig", s.emDataGetConfig)
	mux.HandleFunc("/rpc/Sys.GetStatus", s.sysGetStatus)
	mux.HandleFunc("/rpc/Sys.GetConfig", s.sysGetConfig)
	mux.HandleFunc("/rpc/WiFi.GetStatus", s.wifiGetStatus) // legacy alias
	mux.HandleFunc("/rpc/Wifi.GetStatus", s.wifiGetStatus)
	mux.HandleFunc("/rpc/Wifi.GetConfig", s.wifiGetConfig)
	mux.HandleFunc("/rpc/Shelly.ListMethods", s.shellyListMethods)

	// Shelly Gen-2 RPC over WebSocket (used by Shelly apps and some home automation systems)
	mux.HandleFunc("/rpc", s.rpcWebSocket)

	// Legacy identification endpoint – equivalent to Shelly.GetDeviceInfo.
	// Many Shelly clients (including the official Shelly app) probe /shelly
	// first to detect whether a host is a Shelly device.
	mux.HandleFunc("/shelly", s.shellyGetDeviceInfo)

	// Setup UI
	mux.HandleFunc("/setup", s.setupPage)
	mux.HandleFunc("/setup/save", s.setupSave)
	mux.HandleFunc("/setup/scan", s.setupScanWifi)
	mux.HandleFunc("/setup/scan-poweropti", s.setupScanPoweropti)

	// Dedicated WiFi onboarding page (shown via captive portal in AP mode)
	mux.HandleFunc("/wifi", s.wifiSetupPage)
	mux.HandleFunc("/wifi/connect", s.wifiConnect)

	// Status & internal API
	mux.HandleFunc("/api/status", s.apiStatus)
	mux.HandleFunc("/api/logs", s.apiLogs)
	mux.HandleFunc("/api/restart", s.apiRestart)
	mux.HandleFunc("/api/test/poweropti", s.apiTestPoweropti)
	mux.HandleFunc("/api/wifi/forget", s.apiWifiForget)
	mux.HandleFunc("/api/factory-reset", s.apiFactoryReset)
	mux.HandleFunc("/api/update/check", s.apiUpdateCheck)
	mux.HandleFunc("/api/update/apply", s.apiUpdateApply)

	// Captive portal detection – redirects to /wifi when in AP mode so that
	// iOS, Android and Windows automatically open the configuration page.
	mux.HandleFunc("/generate_204", s.captivePortal204)
	mux.HandleFunc("/gen_204", s.captivePortal204) // Android/Chrome alternative
	mux.HandleFunc("/hotspot-detect.html", s.captivePortalHotspot)
	mux.HandleFunc("/library/test/success.html", s.captivePortalHotspot) // Apple legacy
	mux.HandleFunc("/ncsi.txt", s.captivePortalNCSI)
	mux.HandleFunc("/connecttest.txt", s.captivePortalConnectTest)
	mux.HandleFunc("/success.txt", s.captivePortalSuccess) // Apple
	mux.HandleFunc("/redirect", s.captivePortalRedirect)   // Windows 7+

	// Root – redirect based on configuration state
	mux.HandleFunc("/", s.rootHandler)
}

func (s *Server) rootHandler(w http.ResponseWriter, r *http.Request) {
	// In AP mode every request (including DNS-redirected probe URLs) should
	// land on the WiFi setup page so the phone opens the captive portal UI.
	if isCaptivePortalMode() {
		if r.URL.Path == "/" || r.URL.Path == "" {
			http.Redirect(w, r, "/wifi", http.StatusFound)
		} else {
			// Unknown path coming from DNS catch-all (e.g. http://www.google.com/
			// resolved to 192.168.4.1). Redirect to WiFi setup page.
			http.Redirect(w, r, "/wifi", http.StatusFound)
		}
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.statusPage(w, r)
}

// logf records a message to the in-memory ring buffer.
func (s *Server) logf(format string, a ...any) {
	s.logBuffer.printf(format, a...)
}

// RunNotifyBroadcaster watches the poller for new readings and pushes
// NotifyStatus events to all connected WebSocket clients.
// It blocks until ctx is cancelled and should be run in a goroutine.
// If no poller is configured the function returns immediately.
func (s *Server) RunNotifyBroadcaster(ctx context.Context) {
	if s.poller == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.poller.Notify():
			if s.hub.len() == 0 {
				continue
			}
			data, err := s.buildNotifyStatus()
			if err != nil {
				continue
			}
			s.hub.broadcast(data)
		}
	}
}

// jsonHeader sets Content-Type to application/json.
func jsonHeader(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
}
