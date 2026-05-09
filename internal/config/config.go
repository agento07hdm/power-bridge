// Package config handles loading and saving the power-bridge configuration.
package config

import (
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// PhaseDistribution controls how single-phase power is mapped to three phases.
type PhaseDistribution string

const (
	// PhaseEqual spreads total power evenly across L1, L2 and L3.
	PhaseEqual PhaseDistribution = "equal"
	// PhaseL1 assigns all power to L1; L2 and L3 report zero.
	PhaseL1 PhaseDistribution = "l1"
)

// DeviceProfile selects extra quirks for specific battery systems.
type DeviceProfile string

const (
	ProfileMarstek  DeviceProfile = "marstek"
	ProfileNoah     DeviceProfile = "noah"
	ProfileHoymiles DeviceProfile = "hoymiles"
	ProfileStandard DeviceProfile = "standard"
)

// Config is the complete runtime configuration.
type Config struct {
	// Network
	WIFISSID     string `yaml:"wifi_ssid"`
	WIFIPassword string `yaml:"wifi_password"`

	// Poweropti
	PoweroptiIP     string `yaml:"poweropti_ip"`
	PoweroptiAPIKey string `yaml:"poweropti_api_key"`

	// Shelly emulation identity
	ShellyMAC string `yaml:"shelly_mac"`
	Hostname  string `yaml:"hostname"`

	// Behaviour
	DeviceProfile  DeviceProfile     `yaml:"device_profile"`
	PhaseMode      PhaseDistribution `yaml:"phase_mode"`
	PollIntervalS  int               `yaml:"poll_interval_sec"`
	StaleTimeoutS  int               `yaml:"stale_timeout_sec"`
	ListenAddr     string            `yaml:"listen_addr"`

	// Set to true after the first-run setup is completed.
	Configured bool `yaml:"configured"`

	// ExtraPorts lists additional TCP ports the HTTP server will also listen on.
	// Many battery systems (Marstek, Hoymiles) are hardcoded to probe port 1010
	// or 2220 in addition to the standard port 80. Power-bridge binds all listed
	// ports so those systems can connect without manual reconfiguration.
	ExtraPorts []int `yaml:"extra_ports"`
}

// Defaults returns a Config pre-filled with safe default values.
func Defaults() *Config {
	return &Config{
		Hostname:      "shellypro3em-poweropti",
		DeviceProfile: ProfileStandard,
		PhaseMode:     PhaseEqual,
		PollIntervalS: 3,
		StaleTimeoutS: 30,
		ShellyMAC:     "AA:BB:CC:DD:EE:FF",
		ListenAddr:    ":80",
		ExtraPorts:    []int{1010, 2220},
	}
}

// Load reads a YAML config file. If the file does not exist, it returns Defaults.
func Load(path string) (*Config, error) {
	cfg := Defaults()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// macAddrLen is the byte length of a standard 6-octet IEEE 802 MAC address.
const macAddrLen = 6

// ApplyAutodetect fills in placeholder values in cfg using real hardware
// information. It replaces the generic dummy MAC "AA:BB:CC:DD:EE:FF" (or an
// empty MAC) with the actual hardware MAC address of the first available
// non-loopback network interface, and derives the Hostname from that MAC when
// the hostname is still the generic default.
//
// Call this after Load() so that a freshly-installed or factory-reset device
// automatically presents a plausible Shelly identity without requiring manual
// configuration.
func ApplyAutodetect(cfg *Config) {
	const placeholderMAC = "AA:BB:CC:DD:EE:FF"
	const defaultHostname = "shellypro3em-poweropti"

	if cfg.ShellyMAC == "" || cfg.ShellyMAC == placeholderMAC {
		if mac := detectRealMAC(); mac != "" {
			cfg.ShellyMAC = mac
			// Derive hostname from MAC when it is still the generic default.
			if cfg.Hostname == "" || cfg.Hostname == defaultHostname {
				clean := strings.ReplaceAll(strings.ToLower(mac), ":", "")
				cfg.Hostname = "shellypro3em-" + clean
			}
		}
	}
}

// detectRealMAC returns the uppercase colon-separated MAC address of the first
// suitable network interface.  It prefers wlan0 and eth0, then falls back to
// any non-loopback interface that has a 6-byte hardware address.
func detectRealMAC() string {
	for _, name := range []string{"wlan0", "eth0"} {
		iface, err := net.InterfaceByName(name)
		if err == nil && len(iface.HardwareAddr) == macAddrLen {
			return strings.ToUpper(iface.HardwareAddr.String())
		}
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || len(iface.HardwareAddr) != macAddrLen {
			continue
		}
		return strings.ToUpper(iface.HardwareAddr.String())
	}
	return ""
}

// Save writes the config as YAML to path, creating parent directories as needed.
func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
