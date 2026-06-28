package server

// currentBridgeIP prefers wlan0 so AP/station transitions are reflected in
// /api/status more reliably than generic first-interface detection.
func currentBridgeIP() string {
	if ip := getWlan0IP(); ip != "" {
		return ip
	}
	return getLocalIP()
}

// isCaptivePortalMode centralizes AP/setup mode detection for HTTP behavior.
func isCaptivePortalMode() bool {
	if isAPMode() {
		return true
	}
	return getWlan0IP() == apModeIP
}
