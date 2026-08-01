package temporal

import (
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/payloads"
	"go.temporal.io/sdk/converter"
)

func TestDialOptionsRequiresBlobStore(t *testing.T) {
	_, err := dialOptions(Options{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "blob store") {
		t.Fatalf("dialOptions() error = %v, want a missing blob store error", err)
	}
}

func TestDialOptionsAlwaysUsesPayloadChain(t *testing.T) {
	opts, err := dialOptions(Options{DataConverter: converter.GetDefaultDataConverter()}, blobs.NewMemStore(), nil)
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

	var decoded string
	if err := opts.DataConverter.FromPayload(payload, &decoded); err != nil {
		t.Fatalf("FromPayload() error = %v", err)
	}
	if want := strings.Repeat("compressible payload ", 4096); decoded != want {
		t.Errorf("decoded payload = %q, want %q", decoded, want)
	}
}

func TestDialOptionsReplacesCallerConverter(t *testing.T) {
	opts, err := dialOptions(Options{DataConverter: payloads.DataConverter(blobs.NewMemStore(), nil)}, blobs.NewMemStore(), nil)
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
