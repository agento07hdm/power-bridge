package server

// export_test.go exposes internal helpers for use in package-level tests.
// This file is only compiled during testing.

// ExportBuildNotifyStatus calls the unexported buildNotifyStatus method.
func (s *Server) ExportBuildNotifyStatus() ([]byte, error) {
	return s.buildNotifyStatus()
}

// ExportHubBroadcast broadcasts raw data to all connected WebSocket clients.
func (s *Server) ExportHubBroadcast(data []byte) {
	s.hub.broadcast(data)
}

// ExportSetWifiCredentials sets the in-memory WiFi credentials for testing.
func (s *Server) ExportSetWifiCredentials(ssid, password string) {
	s.cfg.WIFISSID = ssid
	s.cfg.WIFIPassword = password
}

// ExportSetAPMode overrides the automatic AP-mode detection for the duration of
// a test. The returned cleanup function restores the original behaviour and
// should be called with defer.
func ExportSetAPMode(ap bool) func() {
	b := ap
	isAPModeOverride = &b
	return func() { isAPModeOverride = nil }
}
