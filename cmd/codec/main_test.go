package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/payloads"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestDecodeRoundTripsAnOffloadedPayload(t *testing.T) {
	t.Parallel()

	store := blobs.NewMemStore()
	value := strings.Repeat("compressible payload ", 4096)
	original, err := converter.GetDefaultDataConverter().ToPayload(value)
	if err != nil {
		t.Fatalf("default ToPayload() error = %v", err)
	}
	encoded, err := payloads.DataConverter(store, nil).ToPayload(value)
	if err != nil {
		t.Fatalf("codec ToPayload() error = %v", err)
	}
	response := servePayloads(t, newHandler(store, []string{"https://temporal.example"}, discardLogger()), "/decode", &commonpb.Payloads{Payloads: []*commonpb.Payload{encoded}})

	if response.Code != http.StatusOK {
		t.Fatalf("POST /decode status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	decoded := decodePayloads(t, response)
	if !proto.Equal(decoded.Payloads[0], original) {
		t.Errorf("decoded payload = %v, want %v", decoded.Payloads[0], original)
	}
}

func TestEncodeRoute(t *testing.T) {
	t.Parallel()

	store := blobs.NewMemStore()
	original, err := converter.GetDefaultDataConverter().ToPayload("codec route round trip")
	if err != nil {
		t.Fatalf("default ToPayload() error = %v", err)
	}
	handler := newHandler(store, []string{"https://temporal.example"}, discardLogger())
	encoded := servePayloads(t, handler, "/encode", &commonpb.Payloads{Payloads: []*commonpb.Payload{original}})
	if encoded.Code != http.StatusOK {
		t.Fatalf("POST /encode status = %d, want %d: %s", encoded.Code, http.StatusOK, encoded.Body.String())
	}
	decoded := servePayloads(t, handler, "/decode", decodePayloads(t, encoded))
	if decoded.Code != http.StatusOK {
		t.Fatalf("POST /decode status = %d, want %d: %s", decoded.Code, http.StatusOK, decoded.Body.String())
	}
	if got := decodePayloads(t, decoded); !proto.Equal(got, &commonpb.Payloads{Payloads: []*commonpb.Payload{original}}) {
		t.Errorf("POST /encode then /decode = %v, want %v", got, original)
	}
}

func TestCORSPreflightAllowsTheConfiguredOrigin(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodOptions, "/decode", nil)
	request.Header.Set("Origin", "https://temporal.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	response := httptest.NewRecorder()

	newHandler(blobs.NewMemStore(), []string{"https://temporal.example"}, discardLogger()).ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Errorf("OPTIONS /decode status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://temporal.example" {
		t.Errorf("Access-Control-Allow-Origin = %q, want configured origin", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Methods"); got != http.MethodPost {
		t.Errorf("Access-Control-Allow-Methods = %q, want %q", got, http.MethodPost)
	}
	if got := response.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, X-Namespace" {
		t.Errorf("Access-Control-Allow-Headers = %q, want namespace header", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want true", got)
	}
}

func TestCodecRoutePassesThroughControlCenterPayloads(t *testing.T) {
	t.Parallel()

	handler := newHandler(blobs.NewMemStore(), []string{"https://temporal.example"}, discardLogger())
	original, err := converter.GetDefaultDataConverter().ToPayload("control center payload")
	if err != nil {
		t.Fatalf("default ToPayload() error = %v", err)
	}
	want := &commonpb.Payloads{Payloads: []*commonpb.Payload{original}}
	body, err := protojson.Marshal(want)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	allowed := httptest.NewRequest(http.MethodPost, "/decode", bytes.NewReader(body))
	allowed.Header.Set("X-Namespace", "software-factory")
	allowedResponse := httptest.NewRecorder()
	handler.ServeHTTP(allowedResponse, allowed)
	if allowedResponse.Code != http.StatusOK {
		t.Errorf("software-factory POST /decode status = %d, want %d", allowedResponse.Code, http.StatusOK)
	}

	controlCenter := httptest.NewRequest(http.MethodPost, "/decode", bytes.NewReader(body))
	controlCenter.Header.Set("X-Namespace", controlCenterTemporalNamespace)
	controlCenterResponse := httptest.NewRecorder()
	handler.ServeHTTP(controlCenterResponse, controlCenter)
	if controlCenterResponse.Code != http.StatusOK {
		t.Errorf("control-center POST /decode status = %d, want %d", controlCenterResponse.Code, http.StatusOK)
	}
	if got := decodePayloads(t, controlCenterResponse); !proto.Equal(got, want) {
		t.Errorf("control-center payloads = %v, want %v", got, want)
	}

	denied := httptest.NewRequest(http.MethodPost, "/decode", bytes.NewReader(body))
	denied.Header.Set("X-Namespace", "unregistered")
	deniedResponse := httptest.NewRecorder()
	handler.ServeHTTP(deniedResponse, denied)
	if deniedResponse.Code != http.StatusForbidden {
		t.Errorf("unregistered POST /decode status = %d, want %d", deniedResponse.Code, http.StatusForbidden)
	}
}

func TestCORSRejectsAnUnknownOrigin(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodOptions, "/decode", nil)
	request.Header.Set("Origin", "https://unknown.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	response := httptest.NewRecorder()

	newHandler(blobs.NewMemStore(), []string{"https://temporal.example"}, discardLogger()).ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Errorf("OPTIONS /decode status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty", got)
	}
}

func TestHealthz(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	newHandler(blobs.NewMemStore(), []string{"https://temporal.example"}, discardLogger()).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Errorf("GET /healthz status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestCodecLogsRequestMetadataWithoutPayloadContents(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	body, err := protojson.Marshal(&commonpb.Payloads{})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/decode", bytes.NewReader(body))
	request.Header.Set("X-Namespace", controlCenterTemporalNamespace)
	response := httptest.NewRecorder()

	newHandler(blobs.NewMemStore(), []string{"https://temporal.example"}, logger).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("POST /decode status = %d, want %d", response.Code, http.StatusOK)
	}
	entry := logs.String()
	for _, want := range []string{"codec request completed", `"path":"/decode"`, `"namespace":"control-center"`, `"status":200`} {
		if !strings.Contains(entry, want) {
			t.Errorf("log entry = %q, want %q", entry, want)
		}
	}
	if strings.Contains(entry, "payloads") {
		t.Errorf("log entry contains payload contents: %q", entry)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func servePayloads(t *testing.T, handler http.Handler, path string, payloads *commonpb.Payloads) *httptest.ResponseRecorder {
	t.Helper()

	body, err := protojson.Marshal(payloads)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("X-Namespace", softwareFactoryTemporalNamespace)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodePayloads(t *testing.T, response *httptest.ResponseRecorder) *commonpb.Payloads {
	t.Helper()

	payloads := &commonpb.Payloads{}
	if err := protojson.Unmarshal(response.Body.Bytes(), payloads); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return payloads
}
