// Package config loads daemon configuration (G1).
//
// Precedence, lowest to highest: defaults → YAML file → environment variables.
// Flags in main override the lot. Everything has a working default so a bare
// `snapfall` starts without any config at all.
package config

import (
	"bytes"
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Config is the daemon's full configuration surface.
type Config struct {
	// OrgID identifies this organization in logs and events.
	OrgID string `yaml:"org_id"`
	// DBPath is the SQLite database location.
	DBPath string `yaml:"db_path"`
	// ManifestDir holds the worker manifests.
	ManifestDir string `yaml:"manifest_dir"`
	// MemoryDir holds Brain's per-job memory files (G4).
	MemoryDir string `yaml:"memory_dir"`
	// LogLevel is one of debug, info, warn, error.
	LogLevel string `yaml:"log_level"`
	// HeartbeatMS is the dummy worker's heartbeat interval.
	HeartbeatMS int `yaml:"heartbeat_ms"`
}

// Defaults returns the configuration a bare daemon runs with.
func Defaults() Config {
	return Config{
		OrgID:       "org_demo",
		DBPath:      "snapfall.db",
		ManifestDir: "manifests",
		MemoryDir:   "memory/jobs",
		LogLevel:    "info",
		HeartbeatMS: 1000,
	}
}

// Load builds the effective config: defaults, overlaid by the YAML file at path
// (if path is "" or the file does not exist, that layer is skipped — a missing
// explicit path is an error, a missing default path is not), overlaid by env vars.
func Load(path string, pathExplicit bool) (Config, error) {
	cfg := Defaults()

	if path != "" {
		raw, err := os.ReadFile(path)
		switch {
		case err == nil:
			// KnownFields: a typo'd key must fail, not silently fall back to a default.
			var fileCfg Config
			dec := yaml.NewDecoder(bytes.NewReader(raw))
			dec.KnownFields(true)
			if err := dec.Decode(&fileCfg); err != nil {
				return Config{}, fmt.Errorf("parsing %s: %w", path, err)
			}
			cfg = overlay(cfg, fileCfg)
		case os.IsNotExist(err) && !pathExplicit:
			// default path, no file: fine
		default:
			return Config{}, fmt.Errorf("reading %s: %w", path, err)
		}
	}

	cfg = overlayEnv(cfg)

	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func overlay(base, over Config) Config {
	if over.OrgID != "" {
		base.OrgID = over.OrgID
	}
	if over.DBPath != "" {
		base.DBPath = over.DBPath
	}
	if over.ManifestDir != "" {
		base.ManifestDir = over.ManifestDir
	}
	if over.MemoryDir != "" {
		base.MemoryDir = over.MemoryDir
	}
	if over.LogLevel != "" {
		base.LogLevel = over.LogLevel
	}
	if over.HeartbeatMS != 0 {
		base.HeartbeatMS = over.HeartbeatMS
	}
	return base
}

func overlayEnv(cfg Config) Config {
	if v := os.Getenv("SNAPFALL_ORG_ID"); v != "" {
		cfg.OrgID = v
	}
	if v := os.Getenv("SNAPFALL_DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("SNAPFALL_MANIFEST_DIR"); v != "" {
		cfg.ManifestDir = v
	}
	if v := os.Getenv("SNAPFALL_MEMORY_DIR"); v != "" {
		cfg.MemoryDir = v
	}
	if v := os.Getenv("SNAPFALL_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("SNAPFALL_HEARTBEAT_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.HeartbeatMS = n
		}
	}
	return cfg
}

func validate(cfg Config) error {
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log_level %q is not one of debug|info|warn|error", cfg.LogLevel)
	}
	if cfg.HeartbeatMS <= 0 {
		return fmt.Errorf("heartbeat_ms must be positive, got %d", cfg.HeartbeatMS)
	}
	if cfg.OrgID == "" {
		return fmt.Errorf("org_id must not be empty")
	}
	return nil
}
