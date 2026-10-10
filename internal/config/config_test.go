package config

import "testing"

func TestStorageConfiguration(t *testing.T) {
	t.Setenv("STORAGE_DRIVER", "")
	t.Setenv("LOCAL_STORAGE_PATH", "")
	t.Setenv("LOCAL_STORAGE_KEY", "")
	t.Setenv("S3_REQUEST_TIMEOUT_SECONDS", "")
	cfg := Load().Storage
	if cfg.RequestTimeoutSeconds != 30 {
		t.Fatalf("unexpected S3 timeout: %d", cfg.RequestTimeoutSeconds)
	}
	if cfg.Driver != "s3" || cfg.LocalPath != "./data/storage" || cfg.LocalKey != "" {
		t.Fatalf("unexpected storage defaults: driver=%q path=%q", cfg.Driver, cfg.LocalPath)
	}
	t.Setenv("STORAGE_DRIVER", "local")
	t.Setenv("LOCAL_STORAGE_PATH", "./test-images")
	t.Setenv("LOCAL_STORAGE_KEY", "local-test-key")
	t.Setenv("S3_REQUEST_TIMEOUT_SECONDS", "120")
	cfg = Load().Storage
	if cfg.Driver != "local" || cfg.LocalPath != "./test-images" || cfg.LocalKey != "local-test-key" {
		t.Fatal("local storage configuration was not loaded")
	}
	if cfg.RequestTimeoutSeconds != 120 {
		t.Fatalf("S3 timeout not loaded: %d", cfg.RequestTimeoutSeconds)
	}
}
