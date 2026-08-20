// Package config holds Flatline daemon configuration.
//
// Precedence (highest wins): flag > environment variable > default.
// The daemon is local-first (ADR-1/ADR-2): it binds loopback only and never
// accepts a non-loopback address.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

const (
	// DefaultListenAddr is the default loopback bind address.
	DefaultListenAddr = "127.0.0.1:8787"

	// EnvListen is the environment variable overriding the listen address.
	EnvListen = "FLATLINE_LISTEN"
	// EnvDBPath is the environment variable overriding the database path.
	EnvDBPath = "FLATLINE_DB"
)

// Config is the resolved daemon configuration.
type Config struct {
	// Listen is the address the daemon binds. Must be loopback.
	Listen string
	// DBPath is the path to the SQLite database file.
	DBPath string
}

// DefaultDBPath returns the XDG-aware default database path:
// $XDG_DATA_HOME/flatline/flatline.db, or ~/.local/share/flatline/flatline.db
// when XDG_DATA_HOME is unset or empty.
func DefaultDBPath() (string, error) {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("config: cannot resolve home directory: %w", err)
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "flatline", "flatline.db"), nil
}

// Load resolves configuration from environment variables and flag values.
// flagListen and flagDBPath are the values the user passed on the command
// line; an empty string means "not set".
func Load(flagListen, flagDBPath string) (*Config, error) {
	listen := flagListen
	if listen == "" {
		listen = os.Getenv(EnvListen)
	}
	if listen == "" {
		listen = DefaultListenAddr
	}
	if err := ValidateListen(listen); err != nil {
		return nil, err
	}

	dbPath := flagDBPath
	if dbPath == "" {
		dbPath = os.Getenv(EnvDBPath)
	}
	if dbPath == "" {
		p, err := DefaultDBPath()
		if err != nil {
			return nil, err
		}
		dbPath = p
	}

	return &Config{Listen: listen, DBPath: dbPath}, nil
}

// ValidateListen rejects any address that is not a loopback address.
// Flatline is local-first: the daemon must never listen on a non-local
// interface (AGENTS.md §7).
func ValidateListen(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("config: invalid listen address %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("config: listen host %q is not an IP address; only explicit loopback addresses are allowed", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("config: listen address %q is not loopback; Flatline only binds 127.0.0.1/::1", addr)
	}
	return nil
}
