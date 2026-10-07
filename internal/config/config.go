package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultAddress = "127.0.0.1:8088"
	DefaultDataDir = "./data"
)

// Config contains process-level settings that are resolved before any
// repository or HTTP server is created. Per-request timeouts and body
// limits live in the runtime configuration document so they can change
// without a restart.
type Config struct {
	Address           string
	DataDir           string
	ReadHeaderTimeout time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

func FromEnv() (Config, error) {
	cfg := Config{
		Address:           valueOrDefault("CELLAR_CONTROL_ADDR", DefaultAddress),
		DataDir:           valueOrDefault("CELLAR_CONTROL_DATA_DIR", DefaultDataDir),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		ShutdownTimeout:   10 * time.Second,
	}
	return cfg.Validate()
}

func (c Config) Validate() (Config, error) {
	c.Address = strings.TrimSpace(c.Address)
	c.DataDir = strings.TrimSpace(c.DataDir)
	if c.Address == "" {
		return Config{}, fmt.Errorf("address cannot be empty")
	}
	if c.DataDir == "" {
		return Config{}, fmt.Errorf("data directory cannot be empty")
	}
	absolute, err := filepath.Abs(c.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve data directory: %w", err)
	}
	c.DataDir = filepath.Clean(absolute)
	if c.ReadHeaderTimeout <= 0 {
		return Config{}, fmt.Errorf("read header timeout must be positive")
	}
	if c.IdleTimeout <= 0 || c.ShutdownTimeout <= 0 {
		return Config{}, fmt.Errorf("idle and shutdown timeouts must be positive")
	}
	return c, nil
}

func (c Config) EnsureDataDir() error {
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	return nil
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
