package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
)

const (
	envCodecListenAddr  = "LISTEN_ADDR"
	envCodecBlobsURL    = "BLOBS_URL"
	envCodecCORSOrigins = "CODEC_CORS_ORIGINS"
)

// Codec is the parsed startup configuration for the Temporal remote codec server.
type Codec struct {
	ListenAddr  string
	BlobsURL    string
	CORSOrigins []string
	LogLevel    slog.Level
}

// LoadCodec reads and parses the remote codec server configuration once at startup.
func LoadCodec() (Codec, error) {
	cfg := Codec{
		ListenAddr: os.Getenv(envCodecListenAddr),
		BlobsURL:   os.Getenv(envCodecBlobsURL),
	}
	if strings.TrimSpace(cfg.ListenAddr) == "" {
		return Codec{}, fmt.Errorf("%s is required: the codec service listener", envCodecListenAddr)
	}
	if strings.TrimSpace(cfg.BlobsURL) == "" {
		return Codec{}, fmt.Errorf("%s is required: the blob service URL", envCodecBlobsURL)
	}

	origins, err := parseOrigins(os.Getenv(envCodecCORSOrigins))
	if err != nil {
		return Codec{}, err
	}
	cfg.CORSOrigins = origins
	level, err := logLevel()
	if err != nil {
		return Codec{}, err
	}
	cfg.LogLevel = level
	return cfg, nil
}

func parseOrigins(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		if origin == "" {
			return nil, fmt.Errorf("%s must contain one or more HTTP(S) origins", envCodecCORSOrigins)
		}
		if origin == "*" {
			return nil, fmt.Errorf("%s must not contain wildcard origins", envCodecCORSOrigins)
		}
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("%s origin %q must be an HTTP(S) origin without credentials, path, query, or fragment", envCodecCORSOrigins, origin)
		}
		if _, found := seen[origin]; found {
			return nil, fmt.Errorf("%s contains duplicate origin %q", envCodecCORSOrigins, origin)
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
	}
	return origins, nil
}
