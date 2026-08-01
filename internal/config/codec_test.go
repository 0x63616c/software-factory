package config

import (
	"strings"
	"testing"
)

func TestLoadCodecParsesConfiguration(t *testing.T) {
	t.Setenv("LISTEN_ADDR", ":8080")
	t.Setenv("BLOBS_URL", "http://blobs.example")
	t.Setenv("CODEC_CORS_ORIGINS", "https://temporal.example, https://cli.example")

	cfg, err := LoadCodec()
	if err != nil {
		t.Fatalf("LoadCodec() error = %v", err)
	}
	if got, want := cfg.CORSOrigins, []string{"https://temporal.example", "https://cli.example"}; !equalStrings(got, want) {
		t.Errorf("CORSOrigins = %q, want %q", got, want)
	}
}

func TestLoadCodecRejectsInvalidCORSOrigins(t *testing.T) {
	for _, origins := range []string{"", "*", "https://temporal.example,", "https://temporal.example/path", "https://temporal.example,https://temporal.example"} {
		t.Run(origins, func(t *testing.T) {
			t.Setenv("LISTEN_ADDR", ":8080")
			t.Setenv("BLOBS_URL", "http://blobs.example")
			t.Setenv("CODEC_CORS_ORIGINS", origins)

			_, err := LoadCodec()
			if err == nil || !strings.Contains(err.Error(), "CODEC_CORS_ORIGINS") {
				t.Fatalf("LoadCodec() error = %v, want CODEC_CORS_ORIGINS error", err)
			}
		})
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
