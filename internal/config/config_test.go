package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fedzzito/power-bridge/internal/config"
)

func TestDefaults(t *testing.T) {
	cfg := config.Defaults()
	if cfg.PollIntervalS <= 0 {
		t.Error("default poll interval must be > 0")
	}
	if cfg.Hostname == "" {
		t.Error("default hostname must not be empty")
	}
	if cfg.PhaseMode != config.PhaseEqual {
		t.Errorf("default phase mode: want %q, got %q", config.PhaseEqual, cfg.PhaseMode)
	}
}

func TestLoadMissingFile(t *testing.T) {
	cfg, err := config.Load("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if cfg == nil {
		t.Fatal("expected defaults, got nil")
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "config.yaml")

	orig := config.Defaults()
	orig.WIFISSID = "TestNet"
	orig.PoweroptiIP = "10.0.0.5"
	orig.Configured = true
	orig.PhaseMode = config.PhaseL1

	if err := config.Save(path, orig); err != nil {
		t.Fatalf("save: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.WIFISSID != orig.WIFISSID {
		t.Errorf("WIFISSID: want %q, got %q", orig.WIFISSID, loaded.WIFISSID)
	}
	if loaded.PoweroptiIP != orig.PoweroptiIP {
		t.Errorf("PoweroptiIP: want %q, got %q", orig.PoweroptiIP, loaded.PoweroptiIP)
	}
	if loaded.Configured != orig.Configured {
		t.Errorf("Configured: want %v, got %v", orig.Configured, loaded.Configured)
	}
	if loaded.PhaseMode != config.PhaseL1 {
		t.Errorf("PhaseMode: want %q, got %q", config.PhaseL1, loaded.PhaseMode)
	}
}
