package payloads

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/work"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"
)

func TestOffloadLayerRoundTripThroughMemStore(t *testing.T) {
	t.Parallel()

	payload := offloadPayload()
	codec := codecFor(newOffloadLayer(blobs.NewMemStore()))

	encoded, err := codec.Encode([]*commonpb.Payload{payload})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if got := string(encoded[0].Metadata[converter.MetadataEncoding]); got != "binary/remote-payload" {
		t.Errorf("encoding = %q, want binary/remote-payload", got)
	}
	if !bytes.HasPrefix(encoded[0].Data, []byte("payloads/")) {
		t.Errorf("encoded data = %q, want payloads key", encoded[0].Data)
	}

	decoded, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !proto.Equal(decoded[0], payload) {
		t.Fatalf("round trip payload = %v, want %v", decoded[0], payload)
	}
}

func TestOffloadLayerStoresUnderThePayloadsBucket(t *testing.T) {
	t.Parallel()

	store := newRecordingStore()
	payload := offloadPayload()
	marshalled, err := proto.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	_, err = codecFor(newOffloadLayer(store)).Encode([]*commonpb.Payload{payload})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	keys, values := store.puts()
	if len(keys) != 1 {
		t.Fatalf("Put calls = %d, want 1", len(keys))
	}
	if keys[0].Bucket != blobs.BucketPayloads {
		t.Errorf("Put bucket = %q, want %q", keys[0].Bucket, blobs.BucketPayloads)
	}
	if want := work.NewPayloadKey(nil, marshalled).String(); keys[0].Path != want {
		t.Errorf("Put path = %q, want %q", keys[0].Path, want)
	}
	if !bytes.Equal(values[0], marshalled) {
		t.Errorf("Put bytes differ from marshalled payload")
	}
}

func TestOffloadLayerGroupsByWorkflow(t *testing.T) {
	t.Parallel()

	store := newRecordingStore()
	codec, ok := codecFor(newOffloadLayer(store)).(converter.PayloadCodecWithSerializationContext)
	if !ok {
		t.Fatal("codec does not support serialization contexts")
	}
	contextualCodec := codec.WithSerializationContext(converter.WorkflowSerializationContext{
		Namespace:  "factory",
		WorkflowID: "workflow-42",
	})

	encoded, err := contextualCodec.Encode([]*commonpb.Payload{offloadPayload()})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.HasPrefix(encoded[0].Data, []byte("payloads/factory/workflow-42/")) {
		t.Errorf("encoded data = %q, want workflow-scoped key", encoded[0].Data)
	}
}

func TestOffloadLayerWithoutContextUsesUnkeyed(t *testing.T) {
	t.Parallel()

	encoded, err := codecFor(newOffloadLayer(blobs.NewMemStore())).Encode([]*commonpb.Payload{offloadPayload()})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.HasPrefix(encoded[0].Data, []byte("payloads/_unkeyed/")) {
		t.Errorf("encoded data = %q, want unkeyed payload key", encoded[0].Data)
	}
}

func TestOffloadLayerPassesThroughTinyPayloads(t *testing.T) {
	t.Parallel()

	store := newRecordingStore()
	payload := &commonpb.Payload{Data: []byte("tiny")}
	encoded, err := codecFor(newOffloadLayer(store)).Encode([]*commonpb.Payload{payload})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if encoded[0] != payload {
		t.Fatal("encode replaced a tiny payload")
	}
	keys, _ := store.puts()
	if len(keys) != 0 {
		t.Errorf("Put calls = %d, want 0", len(keys))
	}
}

func TestOffloadLayerPutIsIdempotent(t *testing.T) {
	t.Parallel()

	store := newRecordingStore()
	codec := codecFor(newOffloadLayer(store))
	payload := offloadPayload()
	if _, err := codec.Encode([]*commonpb.Payload{payload}); err != nil {
		t.Fatalf("first encode: %v", err)
	}
	if _, err := codec.Encode([]*commonpb.Payload{payload}); err != nil {
		t.Fatalf("second encode: %v", err)
	}
	if got := store.blobCount(); got != 1 {
		t.Errorf("stored blobs = %d, want 1", got)
	}
}

func TestOffloadLayerSurfacesStoreErrors(t *testing.T) {
	t.Parallel()

	store := newRecordingStore()
	store.putErr = errOffloadStore
	_, err := codecFor(newOffloadLayer(store)).Encode([]*commonpb.Payload{offloadPayload()})
	if !errors.Is(err, errOffloadStore) {
		t.Errorf("encode error = %v, want wrapped %v", err, errOffloadStore)
	}
}

func BenchmarkOffloadLayerEncode(b *testing.B) {
	store := blobs.NewMemStore()
	codec := codecFor(newOffloadLayer(store))
	payloads := []*commonpb.Payload{offloadPayload()}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := codec.Encode(payloads); err != nil {
			b.Fatalf("encode: %v", err)
		}
	}
}

var errOffloadStore = errors.New("store unavailable")

type recordingStore struct {
	mu      sync.Mutex
	blobs   map[blobs.Key][]byte
	putKeys []blobs.Key
	putErr  error
}

func newRecordingStore() *recordingStore {
	return &recordingStore{blobs: make(map[blobs.Key][]byte)}
}

func (store *recordingStore) Put(_ context.Context, key blobs.Key, value []byte) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	if store.putErr != nil {
		return store.putErr
	}
	store.putKeys = append(store.putKeys, key)
	if _, exists := store.blobs[key]; !exists {
		store.blobs[key] = bytes.Clone(value)
	}
	return nil
}

func (store *recordingStore) Get(_ context.Context, key blobs.Key) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	value, found := store.blobs[key]
	if !found {
		return nil, blobs.ErrNotFound
	}
	return bytes.Clone(value), nil
}

func (store *recordingStore) puts() ([]blobs.Key, [][]byte) {
	store.mu.Lock()
	defer store.mu.Unlock()

	keys := append([]blobs.Key(nil), store.putKeys...)
	values := make([][]byte, len(keys))
	for i, key := range keys {
		values[i] = bytes.Clone(store.blobs[key])
	}
	return keys, values
}

func (store *recordingStore) blobCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()

	return len(store.blobs)
}

func offloadPayload() *commonpb.Payload {
	return &commonpb.Payload{
		Metadata: map[string][]byte{converter.MetadataEncoding: []byte("json/plain")},
		Data:     bytes.Repeat([]byte("payload"), 1024),
	}
}
