package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/0x63616c/software-factory/internal/blobs"
)

func TestPutThenGetRoundTrip(t *testing.T) {
	handler, _ := newTestHandler(t)
	path := "/blobs/payloads/workflow-1/run-1/payload-1"
	want := []byte("payload bytes")

	put := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(want))
	putResponse := httptest.NewRecorder()
	handler.ServeHTTP(putResponse, put)
	if putResponse.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want %d", putResponse.Code, http.StatusNoContent)
	}

	get := httptest.NewRequest(http.MethodGet, path, nil)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", getResponse.Code, http.StatusOK)
	}
	if got := getResponse.Body.Bytes(); !bytes.Equal(got, want) {
		t.Errorf("GET body = %q, want %q", got, want)
	}
}

func TestGetAbsentReturns404(t *testing.T) {
	handler, _ := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/blobs/payloads/workflow-1/run-1/missing", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Errorf("GET status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestInvalidKeyReturns400(t *testing.T) {
	handler, _ := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/blobs/payloads/../etc/passwd", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Errorf("GET status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestHealthzFailsWhenRootIsMissing(t *testing.T) {
	handler, root := newTestHandler(t)
	if err := os.Remove(root); err != nil {
		t.Fatalf("Remove(%q) error = %v", root, err)
	}
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code == http.StatusOK {
		t.Error("GET /healthz status = 200, want non-200 for missing root")
	}
}

func TestOversizedBodyReturns413(t *testing.T) {
	handler, _ := newTestHandler(t)
	request := httptest.NewRequest(http.MethodPut, "/blobs/payloads/workflow-1/run-1/payload-1", io.LimitReader(zeroReader{}, maxBlobSize+1))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("PUT status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
}

func newTestHandler(t *testing.T) (http.Handler, string) {
	t.Helper()

	root := t.TempDir()
	store, err := blobs.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	return newHandler(store, root, slog.New(slog.NewTextHandler(io.Discard, nil))), root
}

type zeroReader struct{}

func (zeroReader) Read(bytes []byte) (int, error) {
	for index := range bytes {
		bytes[index] = 0
	}
	return len(bytes), nil
}
