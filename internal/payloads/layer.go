// Package payloads provides composable payload codec primitives.
package payloads

import (
	"bytes"
	"fmt"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"
)

const codecVersionMetadataKey = "sf-codec-v"

// Layer is one transformation in the payload pipeline.
//
// A Layer never sees a Payload. It cannot forget to preserve metadata, cannot
// mis-tag its output, and cannot decode what another layer wrote.
type Layer interface {
	// Encoding is the value written to the payload's encoding metadata key.
	Encoding() string

	// Apply transforms a marshalled Payload. sc is nil when the SDK gave no
	// serialization context.
	Apply(sc converter.SerializationContext, b []byte) ([]byte, error)

	// Unapply reverses Apply without a serialization context.
	Unapply(b []byte) ([]byte, error)
}

type layerCodec struct {
	layer                Layer
	serializationContext converter.SerializationContext
}

var _ converter.PayloadCodecWithSerializationContext = (*layerCodec)(nil)

func codecFor(layer Layer) converter.PayloadCodec {
	return &layerCodec{layer: layer}
}

func (c *layerCodec) Encode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	result := make([]*commonpb.Payload, len(payloads))
	for i, payload := range payloads {
		marshalled, err := proto.Marshal(payload)
		if err != nil {
			return result, fmt.Errorf("marshal payload %d: %w", i, err)
		}

		transformed, err := c.layer.Apply(c.serializationContext, marshalled)
		if err != nil {
			return result, fmt.Errorf("apply payload layer %q to payload %d: %w", c.layer.Encoding(), i, err)
		}
		if len(transformed) >= len(marshalled) {
			result[i] = payload
			continue
		}

		result[i] = &commonpb.Payload{
			Metadata: map[string][]byte{
				converter.MetadataEncoding: []byte(c.layer.Encoding()),
				codecVersionMetadataKey:    []byte("1"),
			},
			Data: transformed,
		}
	}
	return result, nil
}

func (c *layerCodec) Decode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	result := make([]*commonpb.Payload, len(payloads))
	for i, payload := range payloads {
		if !bytes.Equal(payload.Metadata[converter.MetadataEncoding], []byte(c.layer.Encoding())) {
			result[i] = payload
			continue
		}

		encodedData := append([]byte(nil), payload.Data...)
		marshalled, err := c.layer.Unapply(encodedData)
		if err != nil {
			return result, fmt.Errorf("unapply payload layer %q to payload %d: %w", c.layer.Encoding(), i, err)
		}

		decoded := &commonpb.Payload{}
		if err := proto.Unmarshal(marshalled, decoded); err != nil {
			return result, fmt.Errorf("unmarshal payload %d: %w", i, err)
		}
		result[i] = decoded
	}
	return result, nil
}

func (c *layerCodec) WithSerializationContext(sc converter.SerializationContext) converter.PayloadCodec {
	copy := *c
	copy.serializationContext = sc
	return &copy
}
