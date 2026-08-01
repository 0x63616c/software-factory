package payloads

import (
	"net/http"

	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/telemetry"
	"go.temporal.io/sdk/converter"
)

// Chain is the payload codec pipeline, outermost first.
//
// Order is the contract. converter.NewCodecDataConverter applies codecs LAST
// to FIRST when encoding and first to last when decoding, so this list reads
// outermost-first and a codec added at the FRONT wraps everything already here.
//
// Compression must stay at the back and offload at the front: a future
// encryption layer inserted between them then compresses before encrypting
// (the only order that compresses well) and stores ciphertext rather than
// plaintext.
func Chain(store blobs.Store, m *telemetry.Metrics) []converter.PayloadCodec {
	return []converter.PayloadCodec{
		codecFor(observed(newOffloadLayer(store), m)),
		codecFor(observed(newCompressLayer(), m)),
	}
}

// DataConverter is what every Temporal client in this service uses.
func DataConverter(store blobs.Store, m *telemetry.Metrics) converter.DataConverter {
	return converter.NewCodecDataConverter(converter.GetDefaultDataConverter(), Chain(store, m)...)
}

// Handler is the remote codec server's HTTP handler, over the same chain.
func Handler(store blobs.Store, m *telemetry.Metrics) http.Handler {
	return converter.NewPayloadCodecHTTPHandler(Chain(store, m)...)
}
