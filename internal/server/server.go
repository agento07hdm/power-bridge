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
	httpSrv    *http.Server
	mux        *http.ServeMux
	logBuffer  *ringLog
}

// New creates a Server. poller may be nil if the device is not yet configured.
func New(cfg *config.Config, configPath string, poller *poweropti.Client) *Server {
	s := &Server{
		cfg:        cfg,
		configPath: configPath,
		poller:     poller,
		logBuffer:  newRingLog(200),
	}

	// Parse embedded templates.
	s.tmplSetup = template.Must(
		template.ParseFS(templateFS, "templates/setup.html"),
	)
	s.tmplStatus = template.Must(
		template.ParseFS(templateFS, "templates/status.html"),
	)

	mux := http.NewServeMux()
	s.mux = mux
	s.registerRoutes(mux)

	s.httpSrv = &http.Server{
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	return s
}

// ServeHTTP implements http.Handler, enabling use with httptest.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Listen binds to addr and serves requests; it blocks until the server is shut down.
func (s *Server) Listen(addr string) error {
	s.httpSrv.Addr = addr
	return s.httpSrv.ListenAndServe()
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
	mux.HandleFunc("/rpc/Sys.GetStatus", s.sysGetStatus)
	mux.HandleFunc("/rpc/Sys.GetConfig", s.sysGetConfig)

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

	// Status & internal API
	mux.HandleFunc("/api/status", s.apiStatus)
	mux.HandleFunc("/api/logs", s.apiLogs)
	mux.HandleFunc("/api/restart", s.apiRestart)
	mux.HandleFunc("/api/test/poweropti", s.apiTestPoweropti)

	// Root – redirect based on configuration state
	mux.HandleFunc("/", s.rootHandler)
}

func (s *Server) rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !s.cfg.Configured {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	s.statusPage(w, r)
}

// logf records a message to the in-memory ring buffer.
func (s *Server) logf(format string, a ...any) {
	s.logBuffer.printf(format, a...)
}

// jsonHeader sets Content-Type to application/json.
func jsonHeader(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
}
