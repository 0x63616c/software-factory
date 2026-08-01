package config

import (
	"log/slog"
	"strings"
	"testing"
)

func TestLoadBlobsRequiresStorageRoot(t *testing.T) {
	t.Setenv(envBlobsRoot, "")
	t.Setenv(envBlobsListenAddr, ":8080")
	if _, err := LoadBlobs(); err == nil || !strings.Contains(err.Error(), envBlobsRoot) {
		t.Fatalf("LoadBlobs() error = %v, want missing %s", err, envBlobsRoot)
	}
}

func TestLoadBlobsRequiresListenAddress(t *testing.T) {
	t.Setenv(envBlobsRoot, "/blobs")
	t.Setenv(envBlobsListenAddr, "")
	if _, err := LoadBlobs(); err == nil || !strings.Contains(err.Error(), envBlobsListenAddr) {
		t.Fatalf("LoadBlobs() error = %v, want missing %s", err, envBlobsListenAddr)
	}
}

func TestLoadBlobsParsesLogLevel(t *testing.T) {
	t.Setenv(envBlobsRoot, "/blobs")
	t.Setenv(envBlobsListenAddr, ":8080")
	t.Setenv(envLogLevel, "debug")

	cfg, err := LoadBlobs()
	if err != nil {
		t.Fatalf("LoadBlobs() error = %v", err)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelDebug)
	}
}

func TestLoadBlobsRejectsInvalidLogLevel(t *testing.T) {
	t.Setenv(envBlobsRoot, "/blobs")
	t.Setenv(envBlobsListenAddr, ":8080")
	t.Setenv(envLogLevel, "chatty")
	if _, err := LoadBlobs(); err == nil || !strings.Contains(err.Error(), envLogLevel) {
		t.Fatalf("LoadBlobs() error = %v, want invalid %s", err, envLogLevel)
	}
}
