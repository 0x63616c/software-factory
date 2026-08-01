package payloads

import (
	"time"

	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/telemetry"
	"go.temporal.io/sdk/converter"
)

// observed wraps l so every Apply and Unapply records bytes in, bytes out and duration, labelled by l.Encoding().
func observed(l Layer, m *telemetry.Metrics) Layer {
	return observedLayer{layer: l, metrics: m}
}

type observedLayer struct {
	layer   Layer
	metrics *telemetry.Metrics
}

func (layer observedLayer) Encoding() string {
	return layer.layer.Encoding()
}

func (layer observedLayer) Apply(sc converter.SerializationContext, input []byte) ([]byte, error) {
	startedAt := (clock.System{}).Now()
	output, err := layer.layer.Apply(sc, input)
	layer.record(input, output, startedAt)
	return output, err
}

func (layer observedLayer) Unapply(input []byte) ([]byte, error) {
	startedAt := (clock.System{}).Now()
	output, err := layer.layer.Unapply(input)
	layer.record(input, output, startedAt)
	return output, err
}

func (layer observedLayer) record(input, output []byte, startedAt time.Time) {
	if layer.metrics == nil {
		return
	}
	layer.metrics.PayloadLayerApplied(layer.Encoding(), len(input), len(output), (clock.System{}).Now().Sub(startedAt))
}
