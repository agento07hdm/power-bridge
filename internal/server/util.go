package server

import (
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// apModeIP is the static IP address used in Access Point mode.
const apModeIP = "192.168.4.1"

// apSSID is the Wi-Fi network name broadcast in AP mode.
const apSSID = "power-bridge"

var startTime = time.Now()

func uptimeSeconds() int64 {
	return int64(time.Since(startTime).Seconds())
}

// serviceName is the systemd unit name used by restart/stop operations.
const serviceName = "power-bridge"

// isAPMode returns true when wlan0 has the static AP IP, meaning the bridge is
// in Access Point / setup mode rather than connected to a home network.
func isAPMode() bool {
	return getLocalIP() == apModeIP
}

// getInterfaceMAC returns the uppercase, colon-separated hardware MAC address
// of the named network interface. Returns an empty string on error.
func getInterfaceMAC(iface string) string {
	i, err := net.InterfaceByName(iface)
	if err != nil || len(i.HardwareAddr) == 0 {
		return ""
	}
	return strings.ToUpper(i.HardwareAddr.String())
}

// getLocalIP returns the first non-loopback IPv4 address found on the host,
// falling back to the hostname if none is found.
func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
				if ipNet.IP.To4() != nil {
					return ipNet.IP.String()
				}
			}
		}
	}
	hostname, _ := os.Hostname()
	return hostname
}

// getCPUTemp reads the CPU temperature from the Linux thermal sysfs interface.
// Returns 0.0 if not available (e.g. on non-Linux systems).
func getCPUTemp() float64 {
	data, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if err != nil {
		return 0.0
	}
	val, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	if err != nil {
		return 0.0
	}
	return val / 1000.0 // millidegrees Celsius → degrees Celsius
}

// getWifiRSSI reads the WiFi signal level from /proc/net/wireless.
// Returns -65 dBm as a reasonable fallback if not available.
func getWifiRSSI() int {
	data, err := os.ReadFile("/proc/net/wireless")
	if err != nil {
		return -65
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, ":") {
			continue
		}
		// Format: "wlan0: 0000   45.  -65.   -256. ..."
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}
		rssiStr := strings.TrimRight(parts[3], ".")
		rssi, err := strconv.Atoi(rssiStr)
		if err != nil {
			continue
		}
		return rssi
	}
	return -65
}

// memInfo holds total and free RAM in bytes.
type memInfo struct {
	Total int
	Free  int
}

// getRealRAM reads MemTotal and MemFree from /proc/meminfo.
// Falls back to Pi Zero 2 W defaults (512 MB total) if not available.
func getRealRAM() memInfo {
	const defaultTotal = 512 * 1024 * 1024
	const defaultFree = 256 * 1024 * 1024
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return memInfo{Total: defaultTotal, Free: defaultFree}
	}
	info := memInfo{Total: defaultTotal, Free: defaultFree}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			info.Total = val * 1024 // kB → bytes
		case "MemFree:":
			info.Free = val * 1024 // kB → bytes
		}
	}
	return info
}

// lookupMACFromARP returns the MAC address for a given IP from the kernel ARP table.
// Returns an empty string if not found.
func lookupMACFromARP(ip string) string {
	data, err := os.ReadFile("/proc/net/arp")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		// ARP table columns: IP, HW type, Flags, MAC, Mask, Device
		if len(fields) < 4 || fields[0] != ip {
			continue
		}
		mac := fields[3]
		// Skip incomplete entries (all zeros)
		if mac == "00:00:00:00:00:00" {
			continue
		}
		return strings.ToUpper(mac)
	}
	return ""
}
