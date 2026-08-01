package temporal

import (
	"fmt"

	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/config"
	"github.com/0x63616c/software-factory/internal/payloads"
	"github.com/0x63616c/software-factory/internal/telemetry"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

// Mode selects how much of the payload pipeline is active.
//
// The ladder is one-directional on the way down: full to decode-only is safe,
// decode-only to off is safe, and full to off is not because blobs already
// written become unreadable.
type Mode string

const (
	// ModeOff leaves Temporal payloads in the SDK's default format.
	ModeOff Mode = "off"
	// ModeDecodeOnly decodes payloads written by the full pipeline without encoding new ones.
	ModeDecodeOnly Mode = "decode-only"
	// ModeFull encodes and decodes payloads through the full pipeline.
	ModeFull Mode = "full"
)

// Options is the Temporal client's dial configuration.
type Options = client.Options

// Client is a live Temporal client connection.
type Client = client.Client

// StartWorkflowOptions configures a workflow start.
type StartWorkflowOptions = client.StartWorkflowOptions

// WorkflowRun identifies a workflow execution and retrieves its result.
type WorkflowRun = client.WorkflowRun

// WorkflowRunGetOptions configures workflow result retrieval.
type WorkflowRunGetOptions = client.WorkflowRunGetOptions

// Dial is the only legal way to construct a Temporal client in this service.
func Dial(opts client.Options, store blobs.Store, metrics *telemetry.Metrics) (client.Client, error) {
	configured, err := dialOptions(opts, store, metrics)
	if err != nil {
		return nil, err
	}
	client, err := client.Dial(configured)
	if err != nil {
		return nil, fmt.Errorf("dial Temporal: %w", err)
	}
	return client, nil
}

func mode() (Mode, error) {
	switch configured := config.PayloadCodecMode(); configured {
	case "", string(ModeOff):
		return ModeOff, nil
	case string(ModeDecodeOnly):
		return ModeDecodeOnly, nil
	case string(ModeFull):
		return ModeFull, nil
	default:
		return "", fmt.Errorf("invalid %s value %q", config.PayloadCodecModeEnv, configured)
	}
}

func dialOptions(opts client.Options, store blobs.Store, metrics *telemetry.Metrics) (client.Options, error) {
	configuredMode, err := mode()
	if err != nil {
		return client.Options{}, err
	}

	switch configuredMode {
	case ModeOff:
		opts.DataConverter = converter.GetDefaultDataConverter()
	case ModeDecodeOnly:
		if store == nil {
			return client.Options{}, fmt.Errorf("%s %q requires a blob store", config.PayloadCodecModeEnv, configuredMode)
		}
		opts.DataConverter = converter.NewCodecDataConverter(converter.GetDefaultDataConverter(), decodeOnlyCodec{codecs: payloads.Chain(store, metrics)})
	case ModeFull:
		if store == nil {
			return client.Options{}, fmt.Errorf("%s %q requires a blob store", config.PayloadCodecModeEnv, configuredMode)
		}
		opts.DataConverter = payloads.DataConverter(store, metrics)
	}
	return opts, nil
}

type decodeOnlyCodec struct {
	codecs []converter.PayloadCodec
}

func (codec decodeOnlyCodec) Encode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	return payloads, nil
}

func (codec decodeOnlyCodec) Decode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	decoded := payloads
	for _, next := range codec.codecs {
		var err error
		decoded, err = next.Decode(decoded)
		if err != nil {
			return nil, err
		}
	}
	return decoded, nil
}
