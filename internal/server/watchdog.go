package server

import (
	"context"
	"log"
	"net"
	"os/exec"
	"strings"
	"time"
)

// startupNetworkFallbackDelay is how long to wait after startup before
// verifying that a home-network connection was established. This gives
// NetworkManager / wpa_supplicant enough time to associate (typically
// 15-30 s on a Pi Zero W) while still catching a failed attempt before
// the user gives up waiting.
const startupNetworkFallbackDelay = 90 * time.Second

// networkWatchdogInterval is how often the long-running watchdog re-checks
// connectivity while in WiFi station mode.
const networkWatchdogInterval = 30 * time.Second

// networkWatchdogDisconnectTimeout is how long the Pi may be without a
// working home-network connection before the watchdog forces AP mode so the
// user can reconfigure without needing physical access.
const networkWatchdogDisconnectTimeout = 10 * time.Minute

// RunStartupNetworkFallback waits startupNetworkFallbackDelay after the
// service starts and then, if a home-network WiFi SSID is configured but
// no routable connection exists, forces AP mode so the user can reach the
// setup page without a serial console or physical access.
//
// This catches the case where the Pi reboots during a WiFi transition and
// NetworkManager fails to reconnect (e.g. wrong password, SSID out of range).
//
// Call in a goroutine; returns when ctx is cancelled.
func (s *Server) RunStartupNetworkFallback(ctx context.Context) {
	// Only meaningful when a WiFi SSID has been saved – an unconfigured device
	// is already in AP mode and needs no fallback.
	if s.cfg.WIFISSID == "" {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(startupNetworkFallbackDelay):
	}
	if isAPMode() {
		// Already in AP mode (e.g. wifi-check.sh restored it) – nothing to do.
		return
	}
	if isNetworkConnected() {
		s.logf("startup-fallback: home network connected – no action needed")
		return
	}
	s.logf("startup-fallback: WiFi not connected after %v – forcing AP mode for recovery",
		startupNetworkFallbackDelay)
	log.Printf("startup-fallback: no network after %v, forcing AP mode", startupNetworkFallbackDelay)
	enableAPMode()
}

// RunNetworkWatchdog periodically checks whether the Pi is still reachable on
// the home network. If connectivity is lost for networkWatchdogDisconnectTimeout
// the watchdog forces AP mode so the user can reconfigure the device.
//
// The watchdog is a no-op while already in AP mode or when no WiFi SSID has
// been configured (fresh out-of-box state). Call in a goroutine; returns when
// ctx is cancelled.
func (s *Server) RunNetworkWatchdog(ctx context.Context) {
	if s.cfg.WIFISSID == "" {
		return
	}
	var disconnectedSince time.Time
	ticker := time.NewTicker(networkWatchdogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// No-op while in setup (AP) mode – the user is already able to reach us.
			if isAPMode() {
				disconnectedSince = time.Time{}
				continue
			}
			if isNetworkConnected() {
				if !disconnectedSince.IsZero() {
					s.logf("watchdog: home network reconnected after %v",
						time.Since(disconnectedSince).Round(time.Second))
				}
				disconnectedSince = time.Time{}
				continue
			}
			// Lost connectivity.
			if disconnectedSince.IsZero() {
				disconnectedSince = time.Now()
				s.logf("watchdog: home network lost – will force AP mode after %v",
					networkWatchdogDisconnectTimeout)
				log.Printf("watchdog: network lost at %v", disconnectedSince.Format(time.RFC3339))
				continue
			}
			if time.Since(disconnectedSince) >= networkWatchdogDisconnectTimeout {
				s.logf("watchdog: no home network for >%v – forcing AP mode for recovery",
					networkWatchdogDisconnectTimeout)
				log.Printf("watchdog: forcing AP mode after %v without connection",
					time.Since(disconnectedSince).Round(time.Second))
				enableAPMode()
				disconnectedSince = time.Time{} // reset so we don't loop
			}
		}
	}
}

// isNetworkConnected returns true when wlan0 has a routable (non-AP,
// non-link-local) IPv4 address, confirmed via NetworkManager when available.
func isNetworkConnected() bool {
	if hasNmcli() {
		// "100 (connected)" when NM considers the device fully associated.
		out, err := exec.Command("nmcli", "-g", "GENERAL.STATE",
			"device", "show", "wlan0").Output()
		if err == nil {
			state := strings.TrimSpace(string(out))
			if !strings.HasPrefix(state, "100") {
				return false
			}
			// NM reports connected – verify the IP is routable and not the AP address.
			ip := getWlan0IP()
			return ip != "" && ip != apModeIP && !strings.HasPrefix(ip, "169.254.")
		}
	}
	// Fallback: inspect IP directly.
	ip := getWlan0IP()
	return ip != "" && ip != apModeIP && !strings.HasPrefix(ip, "169.254.")
}

// getWlan0IP returns the first IPv4 address assigned to wlan0, or "" if none.
func getWlan0IP() string {
	iface, err := net.InterfaceByName("wlan0")
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() != nil {
			return ipNet.IP.String()
		}
	}
	return ""
}
