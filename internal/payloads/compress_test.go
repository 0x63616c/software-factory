package payloads

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"
)

func TestCompressLayerRoundTrip(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "empty", data: []byte{}},
		{name: "one byte", data: []byte{1}},
		{name: "repetitive mebibyte", data: bytes.Repeat([]byte("z"), 1<<20)},
	} {
		t.Run(test.name, func(t *testing.T) {
			layer := newCompressLayer()
			compressed, err := layer.Apply(nil, test.data)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			uncompressed, err := layer.Unapply(compressed)
			if err != nil {
				t.Fatalf("unapply: %v", err)
			}
			if !bytes.Equal(uncompressed, test.data) {
				t.Fatalf("round trip = %d bytes, want %d bytes", len(uncompressed), len(test.data))
			}
		})
	}
}

func TestCompressLayerEncoding(t *testing.T) {
	t.Parallel()

	if got := newCompressLayer().Encoding(); got != "binary/zstd" {
		t.Errorf("encoding = %q, want %q", got, "binary/zstd")
	}
}

func TestCompressLayerThroughCodec(t *testing.T) {
	t.Parallel()

	payload := compressPayload()
	codec := codecFor(newCompressLayer())
	encoded, err := codec.Encode([]*commonpb.Payload{payload})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !proto.Equal(decoded[0], payload) {
		t.Fatalf("round trip payload = %v, want %v", decoded[0], payload)
	}
}

func TestCompressLayerIsConcurrencySafe(t *testing.T) {
	t.Parallel()

	const goroutines = 8
	const iterations = 32

	layer := newCompressLayer()
	errors := make(chan error, goroutines)
	var group sync.WaitGroup
	for worker := range goroutines {
		group.Go(func() {
			data := bytes.Repeat([]byte{byte(worker)}, 32*1024)
			for range iterations {
				compressed, err := layer.Apply(nil, data)
				if err != nil {
					errors <- err
					return
				}
				uncompressed, err := layer.Unapply(compressed)
				if err != nil {
					errors <- err
					return
				}
				if !bytes.Equal(uncompressed, data) {
					errors <- errCompressRoundTrip
					return
				}
			}
		})
	}
	group.Wait()
	close(errors)
	for err := range errors {
		t.Errorf("concurrent round trip: %v", err)
	}
}

func TestCompressLayerGoldenWireFormat(t *testing.T) {
	t.Parallel()

	encoded, err := codecFor(newCompressLayer()).Encode([]*commonpb.Payload{compressGoldenPayload()})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if got := string(encoded[0].Metadata[converter.MetadataEncoding]); got != "binary/zstd" {
		t.Fatalf("encoding = %q, want binary/zstd", got)
	}
	goldenPath := filepath.Join("testdata", "compress.golden")
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(encoded[0].Data, want) {
		t.Fatal("encoded payload data differs from frozen golden wire format")
	}
}

var errCompressRoundTrip = &compressRoundTripError{}

type compressRoundTripError struct{}

func (*compressRoundTripError) Error() string {
	return "decompressed bytes differ from input"
}

func compressPayload() *commonpb.Payload {
	return &commonpb.Payload{
		Metadata: map[string][]byte{
			converter.MetadataEncoding: []byte("json/plain"),
			"source":                   []byte("compress-layer-test"),
			"trace":                    []byte("metadata-must-survive"),
		},
		Data: bytes.Repeat([]byte("z"), 64*1024),
	}
}

func compressGoldenPayload() *commonpb.Payload {
	return &commonpb.Payload{Data: bytes.Repeat([]byte("z"), 64*1024)}
}
