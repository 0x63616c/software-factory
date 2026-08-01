package main

import (
	"log/slog"
	"os"

	"k8s.io/klog/v2"
)

// newLogger is the process's one logger: JSON on stdout, so the cluster's Loki
// pipeline picks it up as structured records rather than as text it has to
// guess at.
//
// Everything below this line is handed this logger. Nothing constructs its own.
func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

// bridgeKlog routes client-go's own logging into ours.
//
// client-go logs through klog, which writes unstructured lines to stderr and
// knows nothing about our logger. Left alone it is the one component in this
// process whose output is invisible to a Loki query filtering on level or
// service — and it is the component that reports the API-server failures worth
// finding. The k8s client sets a per-instance warning handler for the apiserver
// warnings it can see; this catches everything else, and it is process-global,
// which is why it lives here rather than in internal/clients/k8s.
//
// It is also why cmd/ is the only place that may import klog: the Kubernetes
// worldview is otherwise sealed in internal/clients/k8s by depguard, and the
// composition root sits outside that wall on purpose.
func bridgeKlog(logger *slog.Logger) {
	klog.SetSlogLogger(logger)
}
