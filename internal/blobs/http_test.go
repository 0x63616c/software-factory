package blobs

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPStoreRoundTrip(t *testing.T) {
	values := make(map[string][]byte)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodPut:
			value, err := io.ReadAll(request.Body)
			if err != nil {
				t.Errorf("read request body: %v", err)
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			values[request.URL.Path] = value
			writer.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			value, found := values[request.URL.Path]
			if !found {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = writer.Write(value)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	store, err := NewHTTPStore(server.URL, nil)
	if err != nil {
		t.Fatalf("NewHTTPStore() error = %v", err)
	}

	key := newTestKey(t, "workflow-1/run-1/payload-1")
	want := []byte("payload")
	if err := store.Put(t.Context(), key, want); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	got, err := store.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Get() = %q, want %q", got, want)
	}
}

func TestHTTPStorePutTargetsTheExpectedRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut {
			t.Errorf("method = %q, want %q", request.Method, http.MethodPut)
		}
		if request.URL.Path != "/blobs/payloads/workflow-1/run-1/payload-1" {
			t.Errorf("path = %q, want exact blob route", request.URL.Path)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	store, err := NewHTTPStore(server.URL, nil)
	if err != nil {
		t.Fatalf("NewHTTPStore() error = %v", err)
	}

	if err := store.Put(t.Context(), newTestKey(t, "workflow-1/run-1/payload-1"), []byte("payload")); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
}

func TestHTTPStoreGetNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	store, err := NewHTTPStore(server.URL, nil)
	if err != nil {
		t.Fatalf("NewHTTPStore() error = %v", err)
	}

	_, err = store.Get(t.Context(), newTestKey(t, "workflow-1/run-1/missing"))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get() error = %v, want error wrapping ErrNotFound", err)
	}
}

func TestHTTPStoreSurfacesServerErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte("blob service unavailable"))
	}))
	defer server.Close()

	store, err := NewHTTPStore(server.URL, nil)
	if err != nil {
		t.Fatalf("NewHTTPStore() error = %v", err)
	}

	err = store.Put(t.Context(), newTestKey(t, "workflow-1/run-1/payload-1"), []byte("payload"))
	if err == nil {
		t.Fatal("Put() error = nil, want server error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("Put() error = %v, want status 500", err)
	}
	if !strings.Contains(err.Error(), "blob service unavailable") {
		t.Errorf("Put() error = %v, want response body snippet", err)
	}
}

func TestNewHTTPStoreRejectsBadBaseURL(t *testing.T) {
	for _, baseURL := range []string{"", "://nope"} {
		t.Run(baseURL, func(t *testing.T) {
			_, err := NewHTTPStore(baseURL, nil)
			if err == nil {
				t.Fatalf("NewHTTPStore(%q) error = nil, want invalid URL error", baseURL)
			}
		})
	}
}

func TestHTTPStoreDefaultClientHasATimeout(t *testing.T) {
	store, err := NewHTTPStore("http://blob-service", nil)
	if err != nil {
		t.Fatalf("NewHTTPStore() error = %v", err)
	}

	httpStore, ok := store.(*httpStore)
	if !ok {
		t.Fatalf("NewHTTPStore() = %T, want *httpStore", store)
	}
	if httpStore.client.Timeout <= 0 {
		t.Errorf("default client timeout = %v, want positive timeout", httpStore.client.Timeout)
	}
}
