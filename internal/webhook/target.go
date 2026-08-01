package webhook

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/0x63616c/software-factory/internal/store"
)

// TargetHandler is the post-cutover GitHub webhook consumer. It authenticates
// and durably deduplicates relay deliveries but owns no Ticket transition:
// only the target workflow's Confirmed Merge transaction may complete work.
// It remains additive and unwired until the cutover activates it.
type TargetHandler struct {
	secret     []byte
	deliveries store.WebhookDeliveryAcknowledger
	logger     *slog.Logger
	metrics    *targetMetrics
}

// NewTargetHandler constructs the inert target consumer without changing the
// live legacy Handler registration.
func NewTargetHandler(secret []byte, deliveries store.WebhookDeliveryAcknowledger, logger *slog.Logger, registry prometheus.Registerer) *TargetHandler {
	return &TargetHandler{secret: secret, deliveries: deliveries, logger: logger, metrics: newTargetMetrics(registry)}
}

// ServeHTTP accepts every authenticated relay event, records its delivery id
// once, and intentionally performs no event-specific state transition.
func (h *TargetHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil || !validSignature(r.Header.Get("x-hub-signature-256"), body, h.secret) {
		h.metrics.rejected.Inc()
		h.logger.Warn("target factory webhook rejected", slog.String("reason", "invalid_signature"))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	h.metrics.received.Inc()

	deliveryID := r.Header.Get("x-github-delivery")
	if deliveryID == "" {
		h.metrics.ignored.Inc()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	first, err := h.deliveries.RecordWebhookDelivery(r.Context(), deliveryID)
	if err != nil {
		h.logger.Error("target factory webhook: recording delivery failed",
			slog.String("delivery_id", deliveryID), slog.String("error", err.Error()))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !first {
		h.metrics.duplicates.Inc()
	}
	h.logger.Info("target factory webhook accepted",
		slog.String("delivery_id", deliveryID),
		slog.String("event", r.Header.Get("x-github-event")),
		slog.Bool("first_delivery", first),
	)
	w.WriteHeader(http.StatusNoContent)
}

type targetMetrics struct {
	received   prometheus.Counter
	rejected   prometheus.Counter
	ignored    prometheus.Counter
	duplicates prometheus.Counter
}

func newTargetMetrics(registry prometheus.Registerer) *targetMetrics {
	values := &targetMetrics{
		received:   prometheus.NewCounter(prometheus.CounterOpts{Namespace: "factory_target_webhook", Name: "deliveries_received_total", Help: "Authenticated GitHub deliveries received by the target consumer."}),
		rejected:   prometheus.NewCounter(prometheus.CounterOpts{Namespace: "factory_target_webhook", Name: "deliveries_rejected_total", Help: "Deliveries rejected by target-consumer signature verification."}),
		ignored:    prometheus.NewCounter(prometheus.CounterOpts{Namespace: "factory_target_webhook", Name: "deliveries_ignored_total", Help: "Authenticated target-consumer deliveries without a delivery id."}),
		duplicates: prometheus.NewCounter(prometheus.CounterOpts{Namespace: "factory_target_webhook", Name: "deliveries_duplicate_total", Help: "Already-recorded deliveries accepted without repeating work."}),
	}
	registry.MustRegister(values.received, values.rejected, values.ignored, values.duplicates)
	return values
}
