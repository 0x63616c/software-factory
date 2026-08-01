package payloads

import (
	"fmt"

	"github.com/klauspost/compress/zstd"
	"go.temporal.io/sdk/converter"
)

var (
	compressEncoder, compressEncoderErr = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	compressDecoder, compressDecoderErr = zstd.NewReader(nil)
)

type compressLayer struct{}

// newCompressLayer returns the pipeline's compression layer.
func newCompressLayer() Layer {
	return compressLayer{}
}

func (compressLayer) Encoding() string {
	return "binary/zstd"
}

func (compressLayer) Apply(_ converter.SerializationContext, b []byte) ([]byte, error) {
	if compressEncoderErr != nil {
		return nil, fmt.Errorf("initialize zstd encoder: %w", compressEncoderErr)
	}
	return compressEncoder.EncodeAll(b, nil), nil
}

func (compressLayer) Unapply(b []byte) ([]byte, error) {
	if compressDecoderErr != nil {
		return nil, fmt.Errorf("initialize zstd decoder: %w", compressDecoderErr)
	}
	decoded, err := compressDecoder.DecodeAll(b, nil)
	if err != nil {
		return nil, fmt.Errorf("decompress zstd payload: %w", err)
	}
	return decoded, nil
}
