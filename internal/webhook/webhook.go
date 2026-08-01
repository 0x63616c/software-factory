// Package webhook is the factory's own GitHub webhook consumer (#557): the
// event that drives ADR-0012's dependency engine. A merged pull request marks
// its Ticket done, which is the one thing that ever lets a downstream Ticket
// become ready — nothing else in this service does.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

// maxBodyBytes caps a delivery the same way the relay does — generous for any
// real GitHub pull_request payload, small enough that a hostile or malformed
// sender cannot force this process to buffer without bound.
const maxBodyBytes = 2 * 1024 * 1024

// pullRequestEvent is the only shape this consumer reads out of a
// pull_request delivery. Every other field GitHub sends is ignored.
type pullRequestEvent struct {
	Action      string `json:"action"`
	PullRequest struct {
		Merged bool `json:"merged"`
		Head   struct {
			Ref string `json:"ref"`
		} `json:"head"`
	} `json:"pull_request"`
}

// Handler is the factory's one webhook consumer HTTP surface. It is not
// behind the API's Cloudflare-Access/bearer middleware (see cmd/api/main.go):
// its caller is the relay (#535), not a human or an agent, and it
// authenticates the delivery itself, by HMAC, exactly as the relay does.
type Handler struct {
	secret  []byte
	tickets store.WebhookDeliveryRecorder
	logger  *slog.Logger
	metrics *metrics
}

// NewHandler constructs the webhook consumer. secret is the same GitHub App
// webhook secret the relay verifies deliveries against.
func NewHandler(secret []byte, tickets store.WebhookDeliveryRecorder, logger *slog.Logger, registry prometheus.Registerer) *Handler {
	return &Handler{secret: secret, tickets: tickets, logger: logger, metrics: newMetrics(registry)}
}

// ServeHTTP is the whole contract, in order — the order matters:
//
//  1. Read the raw bytes. The signature is over exactly what was sent, never
//     a re-serialised body.
//
//  2. Verify the HMAC in constant time, and reject before touching the
//     database: the response is deliberately terse and identical for every
//     rejection reason, so a caller learns nothing about why and nothing is
//     persisted.
//
//     This duplicates the relay's own verification (internal/relay), and
//     that duplication is a stopgap with an explicit end date, not permanent
//     belt-and-braces: today the cluster's CNI enforces no NetworkPolicy at
//     all, so any in-cluster pod — including a sandbox running a model
//     against attacker-influenceable issue text — can POST straight at this
//     Service. Remove this verification once #532 closes that sandbox
//     network hole; see ADR-0012's "in-cluster trust caveat".
//
//  3. Record the delivery and, only the first time it is seen, act — in one
//     Postgres transaction (store.RecordWebhookDeliveryAndTransition), so
//     acknowledging the delivery and having durably acted on it are the same
//     fact. That is deliberately different from spawning the action in a
//     background goroutine after responding: the action here is one small,
//     local, already-idempotent state transition, not a call to GitHub or a
//     model, so doing it inline costs nothing worth trading durability for.
//     A goroutine started after this handler returns could be lost to a
//     process crash between the response and the write — the exact "no
//     durable asynchronous handoff" gap this consumer's first attempt (see
//     #557) flagged as unsolved. Making the database transaction the thing
//     that gates the response removes that gap instead of building a queue
//     to paper over it. Recovery for a failed write still rides on
//     infrastructure ADR-0012 already built for this: the relay retries a
//     non-2xx response three times, and GitHub's "Redeliver" button is the
//     backstop after that — both safe because this consumer is idempotent on
//     the delivery id.
//
//  4. Respond. Every event and action this consumer does not handle —
//     everything except a `pull_request` `closed` — is accepted and ignored,
//     quietly and cheaply, without ever reaching the database.
//
// A pull request closed without merging moves its Ticket to failed, not done:
// a human closing a factory-opened pull request without merging it is
// rejecting the run's work, the same way a blocked or exhausted run already
// fails its own Ticket in workflows/factory_workticket.go. ADR-0012 does not
// settle this case; this is the decision, stated rather than assumed.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil || !validSignature(r.Header.Get("x-hub-signature-256"), body, h.secret) {
		h.metrics.rejected.Inc()
		h.logger.Warn("factory webhook rejected", slog.String("reason", "invalid_signature"))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	h.metrics.received.Inc()

	event := r.Header.Get("x-github-event")
	deliveryID := r.Header.Get("x-github-delivery")
	if event != "pull_request" || deliveryID == "" {
		h.metrics.ignored.Inc()
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var payload pullRequestEvent
	if err := json.Unmarshal(body, &payload); err != nil {
		// A valid signature but unparsable JSON: GitHub never sends this, but
		// fail closed rather than guess. Never log the body — deliveries
		// carry repository content and user data.
		h.logger.Warn("factory webhook: pull_request payload was not valid JSON",
			slog.String("delivery_id", deliveryID))
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if payload.Action != "closed" {
		h.metrics.ignored.Inc()
		w.WriteHeader(http.StatusNoContent)
		return
	}

	ticketID, ok := work.ParseFactoryTicketBranchName(payload.PullRequest.Head.Ref)
	if !ok {
		// Not a factory-Ticket run's pull request — nothing for this consumer
		// to resolve a Ticket from.
		h.metrics.ignored.Inc()
		w.WriteHeader(http.StatusNoContent)
		return
	}

	target := store.TicketFailed
	if payload.PullRequest.Merged {
		target = store.TicketDone
	}

	outcome, err := h.tickets.RecordWebhookDeliveryAndTransition(
		r.Context(), deliveryID, store.TicketID(ticketID), store.TicketReview, target)
	if err != nil {
		h.logger.Error("factory webhook: recording delivery failed",
			slog.String("delivery_id", deliveryID), slog.Int64("ticket_id", ticketID), slog.String("error", err.Error()))
		// A 5xx tells the relay to retry, and GitHub's Redeliver button is the
		// backstop beyond that — both already safe under this delivery id.
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	h.metrics.outcomes.WithLabelValues(outcomeLabel(outcome)).Inc()
	h.logger.Info("factory webhook: pull_request closed",
		slog.String("delivery_id", deliveryID),
		slog.Int64("ticket_id", ticketID),
		slog.Bool("merged", payload.PullRequest.Merged),
		slog.String("outcome", outcomeLabel(outcome)),
	)
	w.WriteHeader(http.StatusNoContent)
}

// validSignature reports whether signature is a valid "sha256=<hex hmac>" of
// body under secret, checked in constant time. Identical in shape to
// internal/relay's own validSignature — the two packages verify the same
// GitHub HMAC scheme independently rather than sharing a helper, exactly the
// duplication this Handler's own doc comment explains and dates to #532.
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

func outcomeLabel(outcome store.WebhookDeliveryOutcome) string {
	switch outcome {
	case store.WebhookDeliveryApplied:
		return "applied"
	case store.WebhookDeliveryDuplicate:
		return "duplicate"
	case store.WebhookDeliveryStale:
		return "stale"
	default:
		return "unknown"
	}
}

type metrics struct {
	received prometheus.Counter
	rejected prometheus.Counter
	ignored  prometheus.Counter
	outcomes *prometheus.CounterVec
}

func newMetrics(registry prometheus.Registerer) *metrics {
	values := &metrics{
		received: prometheus.NewCounter(prometheus.CounterOpts{Namespace: "factory_webhook", Name: "deliveries_received_total", Help: "Authenticated GitHub deliveries received."}),
		rejected: prometheus.NewCounter(prometheus.CounterOpts{Namespace: "factory_webhook", Name: "deliveries_rejected_total", Help: "Deliveries rejected on signature verification."}),
		ignored:  prometheus.NewCounter(prometheus.CounterOpts{Namespace: "factory_webhook", Name: "deliveries_ignored_total", Help: "Authenticated deliveries this consumer does not act on."}),
		outcomes: prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "factory_webhook", Name: "ticket_transitions_total", Help: "Ticket transitions attempted from a pull_request closed delivery, by outcome."}, []string{"outcome"}),
	}
	registry.MustRegister(values.received, values.rejected, values.ignored, values.outcomes)
	return values
}
