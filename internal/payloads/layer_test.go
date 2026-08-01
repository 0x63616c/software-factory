package payloads

import (
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"
)

type reverseLayer struct{}

func (reverseLayer) Encoding() string {
	return "test/reverse"
}

func (reverseLayer) Apply(_ converter.SerializationContext, b []byte) ([]byte, error) {
	return reverse(b), nil
}

func (reverseLayer) Unapply(b []byte) ([]byte, error) {
	return reverse(b), nil
}

type growLayer struct{}

func (growLayer) Encoding() string {
	return "test/grow"
}

func (growLayer) Apply(_ converter.SerializationContext, b []byte) ([]byte, error) {
	return append(append([]byte(nil), b...), 0), nil
}

func (growLayer) Unapply(b []byte) ([]byte, error) {
	return b[:len(b)-1], nil
}

type shrinkingLayer struct {
	context       converter.SerializationContext
	mutateUnapply bool
}

func (*shrinkingLayer) Encoding() string {
	return "test/shrink"
}

func (l *shrinkingLayer) Apply(sc converter.SerializationContext, b []byte) ([]byte, error) {
	l.context = sc
	return b[:len(b)-payloadPadding], nil
}

func (l *shrinkingLayer) Unapply(b []byte) ([]byte, error) {
	unapplied := append([]byte(nil), b...)
	if l.mutateUnapply {
		b[0] ^= 1
	}
	return append(unapplied, make([]byte, payloadPadding)...), nil
}

const payloadPadding = 64

func TestRoundTripPreservesMetadataAndData(t *testing.T) {
	t.Parallel()

	original := testPayload()
	codec := codecFor(reverseLayer{})

	encoded, err := codec.Encode([]*commonpb.Payload{original})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !proto.Equal(decoded[0], original) {
		t.Fatalf("round trip payload = %v, want %v", decoded[0], original)
	}
}

func TestDecodeIgnoresForeignEncoding(t *testing.T) {
	t.Parallel()

	payload := &commonpb.Payload{Metadata: map[string][]byte{converter.MetadataEncoding: []byte("binary/other")}, Data: []byte("foreign")}
	decoded, err := codecFor(reverseLayer{}).Decode([]*commonpb.Payload{payload})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded[0] != payload {
		t.Fatal("decode replaced a foreign payload")
	}
}

func TestEncodePassesThroughWhenNotSmaller(t *testing.T) {
	t.Parallel()

	payload := testPayload()
	encoded, err := codecFor(growLayer{}).Encode([]*commonpb.Payload{payload})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if encoded[0] != payload {
		t.Fatal("encode wrapped a payload that did not get smaller")
	}
}

func TestEncodedPayloadCarriesCodecVersion(t *testing.T) {
	t.Parallel()

	encoded := encodeShrinkingPayload(t, &shrinkingLayer{})
	if got := string(encoded.Metadata[converter.MetadataEncoding]); got != "test/shrink" {
		t.Errorf("encoding = %q, want %q", got, "test/shrink")
	}
	if got := string(encoded.Metadata["sf-codec-v"]); got != "1" {
		t.Errorf("sf-codec-v = %q, want %q", got, "1")
	}
}

func TestSerializationContextReachesApply(t *testing.T) {
	t.Parallel()

	layer := &shrinkingLayer{}
	codec, ok := codecFor(layer).(converter.PayloadCodecWithSerializationContext)
	if !ok {
		t.Fatal("codec does not implement PayloadCodecWithSerializationContext")
	}
	want := converter.WorkflowSerializationContext{Namespace: "test", WorkflowID: "workflow"}
	contextualCodec := codec.WithSerializationContext(want)
	_, err := contextualCodec.Encode([]*commonpb.Payload{shrinkingPayload()})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if layer.context != want {
		t.Errorf("serialization context = %#v, want %#v", layer.context, want)
	}
}

func TestWorksWithoutSerializationContext(t *testing.T) {
	t.Parallel()

	layer := &shrinkingLayer{}
	codec := codecFor(layer)
	payload := shrinkingPayload()
	encoded, err := codec.Encode([]*commonpb.Payload{payload})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if layer.context != nil {
		t.Errorf("serialization context = %#v, want nil", layer.context)
	}
	if !proto.Equal(decoded[0], payload) {
		t.Fatalf("round trip payload = %v, want %v", decoded[0], payload)
	}
}

func TestEncodeDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	payload := shrinkingPayload()
	before := proto.Clone(payload)
	_, err := codecFor(&shrinkingLayer{}).Encode([]*commonpb.Payload{payload})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !proto.Equal(payload, before) {
		t.Fatalf("encode mutated input: got %v, want %v", payload, before)
	}
}

func TestDecodeDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	layer := &shrinkingLayer{mutateUnapply: true}
	codec := codecFor(layer)
	encoded := encodeShrinkingPayload(t, layer)
	before := proto.Clone(encoded)
	_, err := codec.Decode([]*commonpb.Payload{encoded})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !proto.Equal(encoded, before) {
		t.Fatalf("decode mutated input: got %v, want %v", encoded, before)
	}
}

func encodeShrinkingPayload(t *testing.T, layer *shrinkingLayer) *commonpb.Payload {
	t.Helper()

	encoded, err := codecFor(layer).Encode([]*commonpb.Payload{shrinkingPayload()})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return encoded[0]
}

func testPayload() *commonpb.Payload {
	return &commonpb.Payload{
		Metadata: map[string][]byte{
			converter.MetadataEncoding: []byte("json/plain"),
			"source":                   []byte("ticket-test"),
			"trace":                    []byte("keep-me"),
		},
		Data: []byte("payload data"),
	}
}

func shrinkingPayload() *commonpb.Payload {
	payload := testPayload()
	payload.Data = append(payload.Data, make([]byte, payloadPadding)...)
	return payload
}

func reverse(b []byte) []byte {
	reversed := make([]byte, len(b))
	for i := range b {
		reversed[len(b)-1-i] = b[i]
	}
	return reversed
}
