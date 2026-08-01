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
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/0x63616c/software-factory/internal/config"
)

func TestValidSignatureReturnsNoContentAndForwardsEveryTarget(t *testing.T) {
	poster := newFakePoster(http.StatusNoContent)
	handler := newTestHandler(poster, targets())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedRequest([]byte(`{"action":"opened"}`)))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	poster.waitForPosts(t, 2)
}

func TestInvalidSignatureReturnsUnauthorizedAndForwardsNothing(t *testing.T) {
	poster := newFakePoster(http.StatusNoContent)
	var logs bytes.Buffer
	handler := NewHandler([]byte("secret"), targets(), poster, instantClock{}, slog.New(slog.NewTextHandler(&logs, nil)), prometheus.NewRegistry())
	request := httptest.NewRequest(http.MethodPost, "/github", nil)
	request.Header.Set("x-hub-signature-256", "sha256=not-a-signature")
	request.Header.Set("x-github-delivery", "delivery-rejected")
	request.Header.Set("x-github-event", "push")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if count := poster.count(); count != 0 {
		t.Fatalf("posts = %d, want 0", count)
	}
	if !strings.Contains(logs.String(), "delivery_id=delivery-rejected") || !strings.Contains(logs.String(), "event=push") {
		t.Fatalf("log = %q, want delivery id and event", logs.String())
	}
}

func TestAcceptedAndForwardedDeliveryLogsTargetsWithoutPayload(t *testing.T) {
	const payload = `{"secret":"attacker-controlled-payload"}`
	const deliveryID = "delivery-observable"
	const event = "pull_request"
	poster := newFakePoster(http.StatusAccepted)
	logs := newSynchronizedLogBuffer(3)
	handler := NewHandler([]byte("secret"), targets(), poster, instantClock{}, slog.New(slog.NewTextHandler(logs, nil)), prometheus.NewRegistry())
	request := signedRequest([]byte(payload))
	request.Header.Set("x-github-delivery", deliveryID)
	request.Header.Set("x-github-event", event)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	logs.waitForRecords(t, 3)

	output := logs.String()
	for _, want := range []string{"github webhook accepted", "delivery_id=" + deliveryID, "event=" + event} {
		if !strings.Contains(output, want) {
			t.Fatalf("log = %q, want %q", output, want)
		}
	}
	for _, target := range targets() {
		wantTarget := "target=" + target.Name
		found := false
		for _, line := range strings.Split(output, "\n") {
			if strings.Contains(line, "github webhook forwarded") && strings.Contains(line, wantTarget) && strings.Contains(line, "status=202") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("log = %q, want forwarded record for %q with status 202", output, target.Name)
		}
	}
	if strings.Contains(output, payload) {
		t.Fatalf("log = %q, must not contain payload", output)
	}
}

func TestForwardingPreservesRawBodyAndTrustHeaders(t *testing.T) {
	body := []byte{'{', '"', 'x', '"', ':', ' ', '1', '}', '\n'}
	poster := newFakePoster(http.StatusNoContent)
	handler := newTestHandler(poster, targets()[:1])
	request := signedRequest(body)
	request.Header.Set("x-github-delivery", "delivery-1")
	request.Header.Set("x-github-event", "push")
	request.Header.Set("x-github-hook-id", "hook-1")
	request.Header.Set("content-type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	poster.waitForPosts(t, 1)
	delivery := poster.deliveries()[0]
	if string(delivery.Body) != string(body) || delivery.DeliveryID != "delivery-1" || delivery.Event != "push" || delivery.HookID != "hook-1" || delivery.ContentType != "application/json" || delivery.Signature != request.Header.Get("x-hub-signature-256") {
		t.Fatalf("delivery = %+v, want byte-identical body and preserved headers", delivery)
	}
}

func TestFailingTargetDoesNotPreventAnotherTargetReceivingDelivery(t *testing.T) {
	poster := newFakePoster(http.StatusNoContent)
	poster.statuses["down"] = http.StatusInternalServerError
	handler := newTestHandler(poster, []config.RelayTarget{{Name: "down", URL: "http://down"}, {Name: "up", URL: "http://up"}})
	handler.ServeHTTP(httptest.NewRecorder(), signedRequest([]byte("{}")))
	poster.waitForTargetPosts(t, "up", 1)
	poster.waitForTargetPosts(t, "down", 3)
}

func TestServerErrorRetriesThreeTimesAndClientErrorDoesNotRetry(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status int
		want   int
	}{{name: "server error", status: http.StatusInternalServerError, want: 3}, {name: "client error", status: http.StatusBadRequest, want: 1}} {
		t.Run(scenario.name, func(t *testing.T) {
			poster := newFakePoster(scenario.status)
			handler := newTestHandler(poster, targets()[:1])
			handler.ServeHTTP(httptest.NewRecorder(), signedRequest([]byte("{}")))
			poster.waitForPosts(t, scenario.want)
		})
	}
}

func TestClientRejectionIsRecordedAsGivenUpWithoutRetrying(t *testing.T) {
	poster := newFakePoster(http.StatusBadRequest)
	registry := prometheus.NewRegistry()
	var logs bytes.Buffer
	handler := NewHandler(
		[]byte("secret"),
		targets()[:1],
		poster,
		instantClock{},
		slog.New(slog.NewTextHandler(&logs, nil)),
		registry,
	)
	handler.forward(context.Background(), targets()[0], Delivery{DeliveryID: "delivery-4xx", Event: "push"})
	if count := poster.count(); count != 1 {
		t.Fatalf("forward attempts = %d, want 1", count)
	}
	if got := testutil.ToFloat64(handler.metrics.givenUp.WithLabelValues("one")); got != 1 {
		t.Fatalf("forwards given up = %v, want 1", got)
	}
	if !strings.Contains(logs.String(), "target=one") || !strings.Contains(logs.String(), "delivery_id=delivery-4xx") || !strings.Contains(logs.String(), "event=push") {
		t.Fatalf("log = %q, want target, delivery id, and event", logs.String())
	}
}

func TestConfiguredSecondTargetIsForwardedWithoutChangingHandlerCode(t *testing.T) {
	t.Setenv("LISTEN_ADDR", ":8080")
	t.Setenv("METRICS_ADDR", ":9464")
	t.Setenv("GITHUB_BOT_APP__WEBHOOK_SECRET", "secret")
	t.Setenv("RELAY_TARGETS", `[{"name":"control-center","url":"http://control-center"},{"name":"software-factory","url":"http://software-factory"}]`)
	configuration, err := config.LoadRelay()
	if err != nil {
		t.Fatalf("LoadRelay: %v", err)
	}
	poster := newFakePoster(http.StatusNoContent)
	handler := newTestHandler(poster, configuration.Targets)
	handler.ServeHTTP(httptest.NewRecorder(), signedRequest([]byte("{}")))
	poster.waitForTargetPosts(t, "control-center", 1)
	poster.waitForTargetPosts(t, "software-factory", 1)
}

func TestResponseReturnsBeforeSlowForwardCompletes(t *testing.T) {
	poster := newFakePoster(http.StatusNoContent)
	poster.release = make(chan struct{})
	handler := newTestHandler(poster, targets()[:1])
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedRequest([]byte("{}")))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	close(poster.release)
}

func targets() []config.RelayTarget {
	return []config.RelayTarget{{Name: "one", URL: "http://one"}, {Name: "two", URL: "http://two"}}
}

func newTestHandler(poster *fakePoster, relayTargets []config.RelayTarget) *Handler {
	return NewHandler([]byte("secret"), relayTargets, poster, instantClock{}, slog.New(slog.NewTextHandler(io.Discard, nil)), prometheus.NewRegistry())
}

func signedRequest(body []byte) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/github", io.NopCloser(io.LimitReader(newByteReader(body), int64(len(body)))))
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(body)
	request.Header.Set("x-hub-signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return request
}

type fakePoster struct {
	mu       sync.Mutex
	posts    []recordedPost
	statuses map[string]int
	started  chan struct{}
	release  chan struct{}
}

type synchronizedLogBuffer struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	records chan struct{}
}

func newSynchronizedLogBuffer(recordCount int) *synchronizedLogBuffer {
	return &synchronizedLogBuffer{records: make(chan struct{}, recordCount)}
}

func (b *synchronizedLogBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	count, err := b.buffer.Write(value)
	b.records <- struct{}{}
	return count, err
}

func (b *synchronizedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func (b *synchronizedLogBuffer) waitForRecords(t *testing.T, want int) {
	t.Helper()
	for range want {
		select {
		case <-b.records:
		case <-t.Context().Done():
			t.Fatal("timed out waiting for webhook log records")
		}
	}
}

type recordedPost struct {
	target   string
	delivery Delivery
}

func newFakePoster(status int) *fakePoster {
	return &fakePoster{statuses: map[string]int{"one": status, "two": status}, started: make(chan struct{}, 10)}
}

func (p *fakePoster) Post(_ context.Context, target config.RelayTarget, delivery Delivery) (int, error) {
	p.mu.Lock()
	p.posts = append(p.posts, recordedPost{target: target.Name, delivery: delivery})
	p.mu.Unlock()
	p.started <- struct{}{}
	if p.release != nil {
		<-p.release
	}
	if status, ok := p.statuses[target.Name]; ok {
		return status, nil
	}
	return http.StatusNoContent, nil
}

func (p *fakePoster) count() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.posts) }
func (p *fakePoster) deliveries() []Delivery {
	p.mu.Lock()
	defer p.mu.Unlock()
	values := make([]Delivery, len(p.posts))
	for index, post := range p.posts {
		values[index] = post.delivery
	}
	return values
}

func (p *fakePoster) waitForPosts(t *testing.T, want int) {
	t.Helper()
	for range want {
		<-p.started
	}
}

func (p *fakePoster) waitForTargetPosts(t *testing.T, target string, want int) {
	t.Helper()
	for {
		p.mu.Lock()
		count := 0
		for _, post := range p.posts {
			if post.target == target {
				count++
			}
		}
		p.mu.Unlock()
		if count >= want {
			return
		}
		<-p.started
	}
}

type instantClock struct{}

func (instantClock) Now() time.Time                             { return time.Unix(0, 0).UTC() }
func (instantClock) Sleep(context.Context, time.Duration) error { return nil }

type byteReader struct {
	body   []byte
	offset int
}

func newByteReader(body []byte) *byteReader { return &byteReader{body: body} }
func (r *byteReader) Read(dst []byte) (int, error) {
	if r.offset == len(r.body) {
		return 0, io.EOF
	}
	amount := copy(dst, r.body[r.offset:])
	r.offset += amount
	return amount, nil
}
