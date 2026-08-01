package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"k8s.io/klog/v2"
)

// TestBridgeKlogPutsClientGoLoggingInOurPipeline is not parallel and does not
// restore anything: klog's logger is process-global, and this binary wants it
// bridged anyway.
func TestBridgeKlogPutsClientGoLoggingInOurPipeline(t *testing.T) {
	var out bytes.Buffer
	bridgeKlog(slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))

	klog.Info("the apiserver said something worth reading")
	klog.Flush()

	line := strings.TrimSpace(out.String())
	if line == "" {
		t.Fatal("klog output did not reach the logger; client-go's failures would be invisible to a Loki query")
	}

	// Structured, not a line of text that happens to have been copied across:
	// the whole point is that a query can filter it by level and service.
	var record map[string]any
	if err := json.Unmarshal([]byte(firstLine(line)), &record); err != nil {
		t.Fatalf("klog output is not a JSON record: %v\n%s", err, line)
	}
	if msg, _ := record["msg"].(string); !strings.Contains(msg, "worth reading") {
		t.Errorf("record %v does not carry the message", record)
	}
}

func TestNewLoggerWritesJSONAtTheLevelItIsGiven(t *testing.T) {
	t.Parallel()

	// The level is config, and a logger that ignored it would make LOG_LEVEL a
	// setting that reads as applied and does nothing.
	logger := newLogger(slog.LevelWarn)
	if logger.Enabled(t.Context(), slog.LevelInfo) {
		t.Error("a logger built at warn is still logging at info")
	}
	if !logger.Enabled(t.Context(), slog.LevelError) {
		t.Error("a logger built at warn has dropped error")
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
