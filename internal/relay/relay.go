// Package relay verifies inbound GitHub webhooks and independently relays them
// to configured in-cluster consumers.
package relay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/config"
	"github.com/0x63616c/software-factory/internal/retry"

	"github.com/cockroachdb/errors"
)

const (
	maxBodyBytes   = 2 * 1024 * 1024
	maxAttempts    = 3
	attemptTimeout = 5 * time.Second
)

var relayRetryPolicy = retry.Policy{
	InitialDelay: 1 * time.Second,
	Multiplier:   2,
	MaxDelay:     2 * time.Second,
}

// Poster sends one already-authenticated delivery to a configured consumer.
// It returns only the response status that controls retrying, sealing net/http
// inside the production adapter.
type Poster interface {
	Post(context.Context, config.RelayTarget, Delivery) (int, error)
}

// Delivery is the immutable raw GitHub request forwarded to a consumer.
type Delivery struct {
	Body        []byte
	Signature   string
	DeliveryID  string
	Event       string
	HookID      string
	ContentType string
}

// Handler owns the relay's authenticated HTTP surface.
type Handler struct {
	secret  []byte
	targets []config.RelayTarget
	poster  Poster
	clock   clock.Clock
	logger  *slog.Logger
	metrics *metrics
}

// NewHandler constructs an inbound webhook handler. Dependencies remain
// explicit so retries and forwarding can be tested without network or time.
func NewHandler(secret []byte, targets []config.RelayTarget, poster Poster, relayClock clock.Clock, logger *slog.Logger, registry prometheus.Registerer) *Handler {
	return &Handler{
		secret:  secret,
		targets: targets,
		poster:  poster,
		clock:   relayClock,
		logger:  logger,
		metrics: newMetrics(registry),
	}
}

// ServeHTTP acknowledges a valid GitHub webhook before targets are forwarded.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/healthz":
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writer.WriteHeader(http.StatusOK)
		return
	case "/github":
		if request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
	default:
		writer.WriteHeader(http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, maxBodyBytes))
	if err != nil || !validSignature(request.Header.Get("x-hub-signature-256"), body, h.secret) {
		h.logger.Warn("github webhook rejected",
			slog.String("reason", "invalid_signature"),
			slog.String("delivery_id", request.Header.Get("x-github-delivery")),
			slog.String("event", request.Header.Get("x-github-event")),
		)
		h.metrics.rejected.Inc()
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}

	delivery := Delivery{
		Body:        body,
		Signature:   request.Header.Get("x-hub-signature-256"),
		DeliveryID:  request.Header.Get("x-github-delivery"),
		Event:       request.Header.Get("x-github-event"),
		HookID:      request.Header.Get("x-github-hook-id"),
		ContentType: request.Header.Get("content-type"),
	}
	h.metrics.received.Inc()
	h.logger.Info("github webhook accepted",
		slog.String("delivery_id", delivery.DeliveryID),
		slog.String("event", delivery.Event),
	)
	writer.WriteHeader(http.StatusNoContent)

	for _, target := range h.targets {
		go h.forward(context.WithoutCancel(request.Context()), target, delivery)
	}
}

func (h *Handler) forward(ctx context.Context, target config.RelayTarget, delivery Delivery) {
	if err := retry.Retry(ctx, h.clock, maxAttempts, relayRetryPolicy, func(_ context.Context, attempt int) (bool, error) {
		attemptContext, cancel := context.WithTimeout(ctx, attemptTimeout)
		startedAt := h.clock.Now()
		status, err := h.poster.Post(attemptContext, target, delivery)
		cancel()
		h.metrics.attempts.WithLabelValues(target.Name, attemptOutcome(status, err)).Inc()
		h.metrics.duration.WithLabelValues(target.Name).Observe(h.clock.Now().Sub(startedAt).Seconds())
		h.logger.Info("github webhook forwarded",
			slog.String("target", target.Name),
			slog.Int("status", status),
			slog.String("delivery_id", delivery.DeliveryID),
			slog.String("event", delivery.Event),
		)

		if err == nil {
			if status < http.StatusBadRequest {
				return false, nil
			}
			if status < http.StatusInternalServerError {
				return false, nil
			}
		}

		if attempt == maxAttempts-1 {
			return false, err
		}
		return true, err
	}); err == nil {
		return
	}

	h.metrics.givenUp.WithLabelValues(target.Name).Inc()
	h.logger.Error("webhook forwarding gave up",
		slog.String("target", target.Name),
		slog.String("delivery_id", delivery.DeliveryID),
		slog.String("event", delivery.Event),
	)
}

func attemptOutcome(status int, err error) string {
	if err != nil {
		return "error"
	}
	if status >= http.StatusInternalServerError {
		return "server_error"
	}
	if status >= http.StatusBadRequest {
		return "rejected"
	}
	return "success"
}

func validSignature(signature string, body, secret []byte) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(signature, prefix) {
		return false
	}
	received, err := hex.DecodeString(strings.TrimPrefix(signature, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(received, mac.Sum(nil))
}

type metrics struct {
	received prometheus.Counter
	rejected prometheus.Counter
	attempts *prometheus.CounterVec
	givenUp  *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func newMetrics(registry prometheus.Registerer) *metrics {
	values := &metrics{
		received: prometheus.NewCounter(prometheus.CounterOpts{Namespace: "webhook_relay", Name: "deliveries_received_total", Help: "Authenticated GitHub deliveries received."}),
		rejected: prometheus.NewCounter(prometheus.CounterOpts{Namespace: "webhook_relay", Name: "deliveries_rejected_total", Help: "Deliveries rejected before forwarding."}),
		attempts: prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "webhook_relay", Name: "forward_attempts_total", Help: "Forward attempts by target and outcome."}, []string{"target", "outcome"}),
		givenUp:  prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "webhook_relay", Name: "forwards_given_up_total", Help: "Deliveries dropped after target retries."}, []string{"target"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "webhook_relay", Name: "forward_duration_seconds", Help: "Forward attempt duration by target."}, []string{"target"}),
	}
	registry.MustRegister(values.received, values.rejected, values.attempts, values.givenUp, values.duration)
	return values
}

// HTTPPoster forwards deliveries with the standard library HTTP client.
type HTTPPoster struct {
	client *http.Client
}

// NewHTTPPoster constructs the concrete network adapter for the composition root.
func NewHTTPPoster(client *http.Client) *HTTPPoster {
	return &HTTPPoster{client: client}
}

// Post sends raw request bytes and only the headers consumers use to verify it.
func (p *HTTPPoster) Post(ctx context.Context, target config.RelayTarget, delivery Delivery) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL, bytes.NewReader(delivery.Body))
	if err != nil {
		return 0, errors.Wrapf(err, "creating request for target %s delivery %s", target.Name, delivery.DeliveryID)
	}
	request.Header.Set("x-hub-signature-256", delivery.Signature)
	request.Header.Set("x-github-delivery", delivery.DeliveryID)
	request.Header.Set("x-github-event", delivery.Event)
	request.Header.Set("x-github-hook-id", delivery.HookID)
	request.Header.Set("content-type", delivery.ContentType)

	response, err := p.client.Do(request)
	if err != nil {
		return 0, errors.Wrapf(err, "posting target %s delivery %s", target.Name, delivery.DeliveryID)
	}
	status := response.StatusCode
	if closeErr := response.Body.Close(); closeErr != nil {
		return 0, errors.Wrapf(closeErr, "closing response for target %s delivery %s", target.Name, delivery.DeliveryID)
	}
	return status, nil
}
