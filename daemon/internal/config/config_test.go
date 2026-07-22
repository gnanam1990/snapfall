package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_DefaultsWhenNoFileNoEnv(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), false)
	if err != nil {
		t.Fatalf("Load with absent default-path file must not fail: %v", err)
	}
	if cfg != Defaults() {
		t.Errorf("got %+v, want pure defaults", cfg)
	}
}

func TestLoad_ExplicitMissingFileIsAnError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), true); err == nil {
		t.Fatal("an explicitly named config file that does not exist must be an error")
	}
}

func TestLoad_FileOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapfall.yaml")
	os.WriteFile(path, []byte("org_id: org_test\nlog_level: debug\nheartbeat_ms: 250\n"), 0o600)

	cfg, err := Load(path, true)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OrgID != "org_test" || cfg.LogLevel != "debug" || cfg.HeartbeatMS != 250 {
		t.Errorf("file values not applied: %+v", cfg)
	}
	if cfg.DBPath != Defaults().DBPath {
		t.Errorf("unset fields must keep defaults, got %q", cfg.DBPath)
	}
}

func TestLoad_EnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapfall.yaml")
	os.WriteFile(path, []byte("org_id: org_from_file\n"), 0o600)

	t.Setenv("SNAPFALL_ORG_ID", "org_from_env")
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OrgID != "org_from_env" {
		t.Errorf("env must beat file: got %q", cfg.OrgID)
	}
}

// A typo'd key must fail loudly, not silently leave a default in place —
// same rule as the manifest loader.
func TestLoad_UnknownKeyIsAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapfall.yaml")
	os.WriteFile(path, []byte("org_idd: oops\n"), 0o600)

	if _, err := Load(path, true); err == nil {
		t.Fatal("unknown config keys must be rejected")
	}
}

func TestLoad_ValidatesLogLevel(t *testing.T) {
	t.Setenv("SNAPFALL_LOG_LEVEL", "loud")
	if _, err := Load("", false); err == nil {
		t.Fatal("an invalid log level must be rejected")
	}
}
