package temporal

import (
	"os"
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/config"
	"github.com/0x63616c/software-factory/internal/payloads"
	"go.temporal.io/sdk/converter"
)

func TestModeDefaultsToOff(t *testing.T) {
	t.Setenv(config.PayloadCodecModeEnv, "configured")
	if err := os.Unsetenv(config.PayloadCodecModeEnv); err != nil {
		t.Fatalf("unsetting %s: %v", config.PayloadCodecModeEnv, err)
	}

	got, err := mode()
	if err != nil {
		t.Fatalf("mode() error = %v", err)
	}
	if got != ModeOff {
		t.Errorf("mode() = %q, want %q", got, ModeOff)
	}
}

func TestOffModeUsesTheDefaultConverter(t *testing.T) {
	t.Setenv(config.PayloadCodecModeEnv, string(ModeOff))

	opts, err := dialOptions(Options{}, nil, nil)
	if err != nil {
		t.Fatalf("dialOptions() error = %v", err)
	}
	payload, err := opts.DataConverter.ToPayload("plain")
	if err != nil {
		t.Fatalf("ToPayload() error = %v", err)
	}
	if got := string(payload.Metadata[converter.MetadataEncoding]); got != "json/plain" {
		t.Errorf("encoding = %q, want json/plain", got)
	}
}

func TestFullModeUsesTheChain(t *testing.T) {
	t.Setenv(config.PayloadCodecModeEnv, string(ModeFull))

	opts, err := dialOptions(Options{}, blobs.NewMemStore(), nil)
	if err != nil {
		t.Fatalf("dialOptions() error = %v", err)
	}
	payload, err := opts.DataConverter.ToPayload(strings.Repeat("compressible payload ", 4096))
	if err != nil {
		t.Fatalf("ToPayload() error = %v", err)
	}
	if got := string(payload.Metadata[converter.MetadataEncoding]); got != "binary/remote-payload" {
		t.Errorf("encoding = %q, want binary/remote-payload", got)
	}
}

func TestDecodeOnlyModeDecodesButDoesNotEncode(t *testing.T) {
	t.Setenv(config.PayloadCodecModeEnv, string(ModeDecodeOnly))

	store := blobs.NewMemStore()
	opts, err := dialOptions(Options{}, store, nil)
	if err != nil {
		t.Fatalf("dialOptions() error = %v", err)
	}
	want := strings.Repeat("compressible payload ", 4096)
	encoded, err := payloads.DataConverter(store, nil).ToPayload(want)
	if err != nil {
		t.Fatalf("encoding through full chain: %v", err)
	}
	var got string
	if err := opts.DataConverter.FromPayload(encoded, &got); err != nil {
		t.Fatalf("decoding through decode-only converter: %v", err)
	}
	if got != want {
		t.Errorf("decoded value = %q, want %q", got, want)
	}

	plain, err := opts.DataConverter.ToPayload(want)
	if err != nil {
		t.Fatalf("encoding through decode-only converter: %v", err)
	}
	if got := string(plain.Metadata[converter.MetadataEncoding]); got != "json/plain" {
		t.Errorf("encoding = %q, want json/plain", got)
	}
}

func TestDialOverridesACallerSuppliedConverter(t *testing.T) {
	t.Setenv(config.PayloadCodecModeEnv, string(ModeOff))

	opts, err := dialOptions(Options{DataConverter: payloads.DataConverter(blobs.NewMemStore(), nil)}, nil, nil)
	if err != nil {
		t.Fatalf("dialOptions() error = %v", err)
	}
	payload, err := opts.DataConverter.ToPayload(strings.Repeat("compressible payload ", 4096))
	if err != nil {
		t.Fatalf("ToPayload() error = %v", err)
	}
	if got := string(payload.Metadata[converter.MetadataEncoding]); got != "json/plain" {
		t.Errorf("encoding = %q, want json/plain from the default converter", got)
	}
}

func TestUnknownModeIsAnError(t *testing.T) {
	t.Setenv(config.PayloadCodecModeEnv, "unknown")

	_, err := dialOptions(Options{}, nil, nil)
	if err == nil {
		t.Fatal("dialOptions() error = nil, want an invalid mode error")
	}
	if !strings.Contains(err.Error(), config.PayloadCodecModeEnv) || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("dialOptions() error = %q, want the variable and invalid value", err)
	}
}
