package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv(EnvListen, "")
	t.Setenv(EnvDBPath, "")

	cfg, err := Load("", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != DefaultListenAddr {
		t.Errorf("Listen = %q, want %q", cfg.Listen, DefaultListenAddr)
	}
	wantDB, err := DefaultDBPath()
	if err != nil {
		t.Fatalf("DefaultDBPath: %v", err)
	}
	if cfg.DBPath != wantDB {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, wantDB)
	}
}

func TestLoadFlagOverridesEnv(t *testing.T) {
	t.Setenv(EnvListen, "127.0.0.1:9999")
	t.Setenv(EnvDBPath, "/tmp/env.db")

	cfg, err := Load("127.0.0.1:7777", "/tmp/flag.db")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != "127.0.0.1:7777" {
		t.Errorf("Listen = %q, want flag value", cfg.Listen)
	}
	if cfg.DBPath != "/tmp/flag.db" {
		t.Errorf("DBPath = %q, want flag value", cfg.DBPath)
	}
}

func TestLoadEnvOverridesDefault(t *testing.T) {
	t.Setenv(EnvListen, "127.0.0.1:9999")
	t.Setenv(EnvDBPath, "/tmp/env.db")

	cfg, err := Load("", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != "127.0.0.1:9999" {
		t.Errorf("Listen = %q, want env value", cfg.Listen)
	}
	if cfg.DBPath != "/tmp/env.db" {
		t.Errorf("DBPath = %q, want env value", cfg.DBPath)
	}
}

func TestLoadRejectsNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8787", "192.168.1.5:8787", "example.com:8787"} {
		if _, err := Load(addr, ""); err == nil {
			t.Errorf("Load(%q): expected error, got nil", addr)
		}
	}
}

func TestValidateListen(t *testing.T) {
	valid := []string{"127.0.0.1:8787", "[::1]:8787"}
	for _, addr := range valid {
		if err := ValidateListen(addr); err != nil {
			t.Errorf("ValidateListen(%q): unexpected error: %v", addr, err)
		}
	}
	invalid := []string{"0.0.0.0:8787", "10.0.0.1:8787", "not-an-address", "127.0.0.1"}
	for _, addr := range invalid {
		if err := ValidateListen(addr); err == nil {
			t.Errorf("ValidateListen(%q): expected error, got nil", addr)
		}
	}
}

func TestDefaultDBPathXDG(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/tmp/xdg-test")
	got, err := DefaultDBPath()
	if err != nil {
		t.Fatalf("DefaultDBPath: %v", err)
	}
	want := filepath.Join("/tmp/xdg-test", "flatline", "flatline.db")
	if got != want {
		t.Errorf("DefaultDBPath = %q, want %q", got, want)
	}
}

func TestDefaultDBPathHomeFallback(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot resolve home dir: %v", err)
	}
	got, err := DefaultDBPath()
	if err != nil {
		t.Fatalf("DefaultDBPath: %v", err)
	}
	want := filepath.Join(home, ".local", "share", "flatline", "flatline.db")
	if got != want {
		t.Errorf("DefaultDBPath = %q, want %q", got, want)
	}
}
