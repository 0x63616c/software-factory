package payloads

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/0x63616c/software-factory/internal/blobs"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestChainRoundTripsThroughBothLayers(t *testing.T) {
	t.Parallel()

	store := newRecordingStore()
	want := chainValue{Text: string(bytes.Repeat([]byte("compressible payload "), 4096))}
	dataConverter := DataConverter(store, nil)

	payload, err := dataConverter.ToPayload(want)
	if err != nil {
		t.Fatalf("ToPayload() error = %v", err)
	}
	var got chainValue
	if err := dataConverter.FromPayload(payload, &got); err != nil {
		t.Fatalf("FromPayload() error = %v", err)
	}
	if got != want {
		t.Errorf("FromPayload() = %#v, want %#v", got, want)
	}
	if got := store.blobCount(); got != 1 {
		t.Errorf("stored blobs = %d, want 1", got)
	}
}

func TestChainOrderIsOffloadOutermost(t *testing.T) {
	t.Parallel()

	store := newRecordingStore()
	payload, err := DataConverter(store, nil).ToPayload(chainValue{Text: string(bytes.Repeat([]byte("compressible payload "), 4096))})
	if err != nil {
		t.Fatalf("ToPayload() error = %v", err)
	}
	if got := string(payload.Metadata[converter.MetadataEncoding]); got != "binary/remote-payload" {
		t.Fatalf("outer encoding = %q, want binary/remote-payload", got)
	}

	_, values := store.puts()
	if len(values) != 1 {
		t.Fatalf("stored blobs = %d, want 1", len(values))
	}
	stored := &commonpb.Payload{}
	if err := proto.Unmarshal(values[0], stored); err != nil {
		t.Fatalf("unmarshal stored payload: %v", err)
	}
	if got := string(stored.Metadata[converter.MetadataEncoding]); got != "binary/zstd" {
		t.Errorf("stored encoding = %q, want binary/zstd", got)
	}
}

func TestHandlerUsesTheSameChain(t *testing.T) {
	t.Parallel()

	store := blobs.NewMemStore()
	value := chainValue{Text: string(bytes.Repeat([]byte("compressible payload "), 4096))}
	original, err := converter.GetDefaultDataConverter().ToPayload(value)
	if err != nil {
		t.Fatalf("default ToPayload() error = %v", err)
	}
	encoded, err := DataConverter(store, nil).ToPayload(value)
	if err != nil {
		t.Fatalf("codec ToPayload() error = %v", err)
	}
	body, err := protojson.Marshal(&commonpb.Payloads{Payloads: []*commonpb.Payload{encoded}})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/decode", bytes.NewReader(body))
	response := httptest.NewRecorder()

	Handler(store, nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	decoded := &commonpb.Payloads{}
	if err := protojson.Unmarshal(response.Body.Bytes(), decoded); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(decoded.Payloads) != 1 {
		t.Fatalf("response payloads = %d, want 1", len(decoded.Payloads))
	}
	if !proto.Equal(decoded.Payloads[0], original) {
		t.Errorf("decoded payload = %v, want %v", decoded.Payloads[0], original)
	}
}

func TestChainDecodesLegacyPayloads(t *testing.T) {
	t.Parallel()

	want := chainValue{Text: "legacy payload"}
	payload, err := converter.GetDefaultDataConverter().ToPayload(want)
	if err != nil {
		t.Fatalf("default ToPayload() error = %v", err)
	}
	var got chainValue
	if err := DataConverter(blobs.NewMemStore(), nil).FromPayload(payload, &got); err != nil {
		t.Fatalf("FromPayload() error = %v", err)
	}
	if got != want {
		t.Errorf("FromPayload() = %#v, want %#v", got, want)
	}
}

type chainValue struct {
	Text string `json:"text"`
}
