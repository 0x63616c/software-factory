package payloads

import (
	"bytes"
	"errors"
	"testing"

	"go.temporal.io/sdk/converter"
)

func TestObservedLayerToleratesNilMetrics(t *testing.T) {
	t.Parallel()

	layer := observed(observedTestLayer{}, nil)
	input := []byte("payload")
	got, err := layer.Apply(nil, input)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if !bytes.Equal(got, []byte("applied:payload")) {
		t.Errorf("Apply() = %q, want %q", got, "applied:payload")
	}
	got, err = layer.Unapply(got)
	if err != nil {
		t.Fatalf("Unapply() error = %v", err)
	}
	if !bytes.Equal(got, input) {
		t.Errorf("Unapply() = %q, want %q", got, input)
	}
	got, err = layer.Unapply([]byte("missing"))
	if !errors.Is(err, errObservedPrefix) {
		t.Errorf("Unapply() error = %v, want %v", err, errObservedPrefix)
	}
	if got != nil {
		t.Errorf("Unapply() output = %q, want nil on error", got)
	}
}

type observedTestLayer struct{}

var errObservedPrefix = errors.New("missing applied prefix")

func (observedTestLayer) Encoding() string { return "test/observed" }

func (observedTestLayer) Apply(_ converter.SerializationContext, input []byte) ([]byte, error) {
	return append([]byte("applied:"), input...), nil
}

func (observedTestLayer) Unapply(input []byte) ([]byte, error) {
	const prefix = "applied:"
	if !bytes.HasPrefix(input, []byte(prefix)) {
		return nil, errObservedPrefix
	}
	return bytes.TrimPrefix(input, []byte(prefix)), nil
}
