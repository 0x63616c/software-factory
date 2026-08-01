package temporal

import (
	"fmt"

	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/payloads"
	"github.com/0x63616c/software-factory/internal/telemetry"
	"go.temporal.io/sdk/client"
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

func dialOptions(opts client.Options, store blobs.Store, metrics *telemetry.Metrics) (client.Options, error) {
	if store == nil {
		return client.Options{}, fmt.Errorf("temporal payload codec requires a blob store")
	}
	opts.DataConverter = payloads.DataConverter(store, metrics)
	return opts, nil
}
