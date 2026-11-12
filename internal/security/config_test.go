package security

import (
	"testing"
)

func TestLoadConfigFromEnvDefaultsAndMaxFiles(t *testing.T) {
	t.Setenv("LOGLENS_WHITELIST_DIRS", "/var/log")
	t.Setenv("LOGLENS_MAX_FILES", "123")
	t.Setenv("LOGLENS_MAX_LINES", "")
	t.Setenv("LOGLENS_MAX_BYTES", "")
	t.Setenv("LOGLENS_TIMEOUT_MS", "")

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}
	if cfg.Limits.MaxFiles != 123 {
		t.Errorf("MaxFiles = %d, want 123 from env", cfg.Limits.MaxFiles)
	}
	if cfg.Limits.MaxLines != DefaultMaxLines {
		t.Errorf("MaxLines = %d, want default %d", cfg.Limits.MaxLines, DefaultMaxLines)
	}
	if cfg.Limits.MaxBytes != DefaultMaxBytes {
		t.Errorf("MaxBytes = %d, want default %d", cfg.Limits.MaxBytes, DefaultMaxBytes)
	}
}

func TestLoadConfigFromEnvBadMaxFiles(t *testing.T) {
	t.Setenv("LOGLENS_WHITELIST_DIRS", "/var/log")
	t.Setenv("LOGLENS_MAX_FILES", "0") // must be positive

	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("expected error for LOGLENS_MAX_FILES=0")
	}
}

func TestLoadConfigFromEnvMaxFilesDefault(t *testing.T) {
	t.Setenv("LOGLENS_WHITELIST_DIRS", "/var/log")
	if _, err := LoadConfigFromEnv(); err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}
}