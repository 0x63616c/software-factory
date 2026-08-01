package checkpoint

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/0x63616c/software-factory/internal/store"
)

// Factory reads the projected capability when an activity starts, then
// scopes a client to that activity's exact workflow-authorized Attempt.
type Factory struct {
	baseURL        string
	capabilityFile string
	httpClient     *http.Client
	readFile       func(string) ([]byte, error)
}

// NewFactory builds a projected-capability client factory.
func NewFactory(baseURL, capabilityFile string, httpClient *http.Client, readFile func(string) ([]byte, error)) (*Factory, error) {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(capabilityFile) == "" || httpClient == nil || readFile == nil {
		return nil, fmt.Errorf("constructing checkpoint factory: URL, capability file, HTTP client, and file reader are required")
	}
	return &Factory{baseURL: baseURL, capabilityFile: capabilityFile, httpClient: httpClient, readFile: readFile}, nil
}

// Open reads the current projected file and returns one exact-attempt client.
func (factory *Factory) Open(id store.TargetAttemptID) (*Client, error) {
	raw, err := factory.readFile(factory.capabilityFile)
	if err != nil {
		return nil, fmt.Errorf("opening checkpoint client for %s: reading projected capability: %w", id, err)
	}
	client, err := New(factory.baseURL, id, strings.TrimSpace(string(raw)), factory.httpClient)
	if err != nil {
		return nil, fmt.Errorf("opening checkpoint client for %s: %w", id, err)
	}
	return client, nil
}
