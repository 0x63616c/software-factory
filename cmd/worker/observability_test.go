package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestObservabilityServesTheMetricsItIsGiven(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "software_factory_test_total",
		Help: "A metric registered by this test.",
	})
	registry.MustRegister(counter)
	counter.Inc()

	body, status := get(t, observability(registry, func() bool { return false }), pathMetrics)
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", pathMetrics, status)
	}
	// Serving a fresh default registry instead of the one the process records
	// into is the failure this catches: the endpoint answers, the dashboards
	// stay empty.
	if !strings.Contains(body, "software_factory_test_total 1") {
		t.Errorf("%s does not carry the registered metric:\n%s", pathMetrics, body)
	}
}

func TestObservabilityAnswersALivenessProbe(t *testing.T) {
	t.Parallel()

	_, status := get(t, observability(prometheus.NewRegistry(), func() bool { return false }), pathHealthz)
	if status != http.StatusOK {
		t.Errorf("GET %s = %d, want 200; the kubelet would restart a healthy worker", pathHealthz, status)
	}
}

func TestObservabilityServesNothingElse(t *testing.T) {
	t.Parallel()

	// The worker takes its work from Temporal. Anything else answering here
	// would be surface nobody meant to expose.
	if _, status := get(t, observability(prometheus.NewRegistry(), func() bool { return false }), "/"); status != http.StatusNotFound {
		t.Errorf("GET / = %d, want 404", status)
	}
}

func TestObservabilityReadinessTracksCompletedActivation(t *testing.T) {
	t.Parallel()
	ready := false
	handler := observability(prometheus.NewRegistry(), func() bool { return ready })
	if _, status := get(t, handler, pathReadyz); status != http.StatusServiceUnavailable {
		t.Fatalf("GET %s before activation = %d, want 503", pathReadyz, status)
	}
	ready = true
	if _, status := get(t, handler, pathReadyz); status != http.StatusOK {
		t.Fatalf("GET %s after activation = %d, want 200", pathReadyz, status)
	}
}

func get(t *testing.T, handler http.Handler, path string) (body string, status int) {
	t.Helper()

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := server.Client().Get(server.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(raw), resp.StatusCode
}

// TestTheServedPathsAreTheOnesScrapedAndProbed pins both paths against
// literals, because every other assertion in this file asks for pathMetrics or
// pathHealthz — the constants themselves — which a rename satisfies unchanged.
//
// Nothing in this repository is the consumer. The scrape config and the
// Deployment's liveness probe quote these strings, so a rename here is a metric
// that stops arriving and a probe that starts failing, with nothing in CI to
// say so. The request below goes through the real handler with the literal in
// hand, so it fails on a renamed constant and on a mux that stopped routing it.
func TestTheServedPathsAreTheOnesScrapedAndProbed(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]int{"/metrics": 200, "/healthz": 200, "/readyz": 503} {
		if _, status := get(t, observability(prometheus.NewRegistry(), func() bool { return false }), path); status != want {
			t.Errorf("GET %s = %d, want %d; the scrape config and the liveness probe quote this exact path",
				path, status, want)
		}
	}

	if pathMetrics != "/metrics" || pathHealthz != "/healthz" || pathReadyz != "/readyz" {
		t.Errorf("served paths are %q, %q, and %q", pathMetrics, pathHealthz, pathReadyz)
	}
}
