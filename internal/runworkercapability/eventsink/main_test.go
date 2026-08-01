package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

var errWriteCountResponse = errors.New("write count response")

func TestEventSinkAppendsEachJSONLEventBeforeTheNextRequest(t *testing.T) {
	eventsPath := filepath.Join(t.TempDir(), "events.jsonl")
	sink, err := newEventSink(eventsPath)
	if err != nil {
		t.Fatalf("creating sink: %v", err)
	}
	t.Cleanup(func() {
		if err := sink.Close(); err != nil {
			t.Errorf("closing sink: %v", err)
		}
	})
	server := httptest.NewServer(sink)
	t.Cleanup(server.Close)

	postEvent(t, server.URL, `{"type":"thread.started","thread_id":"thread-1"}`)
	assertFileContents(t, eventsPath, "after first request", "{\"type\":\"thread.started\",\"thread_id\":\"thread-1\"}\n")
	postEvent(t, server.URL, `{"type":"turn.completed","usage":{"input_tokens":1}}`)
	assertFileContents(
		t,
		eventsPath,
		"after second request",
		"{\"type\":\"thread.started\",\"thread_id\":\"thread-1\"}\n"+
			"{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1}}\n",
	)

	response, err := http.Get(server.URL + "/count")
	if err != nil {
		t.Fatalf("getting sink count: %v", err)
	}
	count, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading sink count: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("closing sink count response: %v", err)
	}
	if got := string(count); got != "2\n" {
		t.Fatalf("count = %q, want 2", got)
	}
}

func TestEventSinkReportsCountResponseWriteError(t *testing.T) {
	sink := &eventSink{count: 2}

	err := sink.writeCount(errorResponseWriter{})

	if !errors.Is(err, errWriteCountResponse) {
		t.Fatalf("writing count response error = %v, want %v", err, errWriteCountResponse)
	}
}

func postEvent(t *testing.T, baseURL, event string) {
	t.Helper()
	response, err := http.Post(baseURL+"/events", "application/json", bytes.NewBufferString(event))
	if err != nil {
		t.Fatalf("posting event: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("closing event response: %v", err)
	}
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("posting event status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
}

type errorResponseWriter struct{}

func (errorResponseWriter) Header() http.Header {
	return make(http.Header)
}

func (errorResponseWriter) Write([]byte) (int, error) {
	return 0, errWriteCountResponse
}

func (errorResponseWriter) WriteHeader(int) {}

func assertFileContents(t *testing.T, path, point, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading events %s: %v", point, err)
	}
	if string(got) != want {
		t.Fatalf("events %s = %q, want %q", point, got, want)
	}
}
