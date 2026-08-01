package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

const (
	envBlobsRoot       = "BLOBS_ROOT"
	envBlobsListenAddr = "LISTEN_ADDR"
)

// Blobs is the parsed startup configuration for the blob service.
type Blobs struct {
	Root       string
	ListenAddr string
	LogLevel   slog.Level
}

// LoadBlobs reads the blob service configuration once at startup.
func LoadBlobs() (Blobs, error) {
	cfg := Blobs{
		Root:       os.Getenv(envBlobsRoot),
		ListenAddr: os.Getenv(envBlobsListenAddr),
	}
	if strings.TrimSpace(cfg.Root) == "" {
		return Blobs{}, fmt.Errorf("%s is required: the mounted blob storage root", envBlobsRoot)
	}
	if strings.TrimSpace(cfg.ListenAddr) == "" {
		return Blobs{}, fmt.Errorf("%s is required: the blob service listener", envBlobsListenAddr)
	}
	level, err := logLevel()
	if err != nil {
		return Blobs{}, err
	}
	cfg.LogLevel = level
	return cfg, nil
}
