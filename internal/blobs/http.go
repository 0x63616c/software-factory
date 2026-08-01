package blobs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultHTTPClientTimeout = 30 * time.Second
	responseSnippetLimit     = 4 << 10
)

type httpStore struct {
	baseURL *url.URL
	client  *http.Client
}

// NewHTTPStore returns a Store backed by a blob service at baseURL.
func NewHTTPStore(baseURL string, client *http.Client) (Store, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("blob service base URL is empty")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse blob service base URL %q: %w", baseURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("blob service base URL %q has unsupported scheme %q", baseURL, parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("blob service base URL %q has no host", baseURL)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("blob service base URL %q must not contain a query or fragment", baseURL)
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPClientTimeout}
	}

	return &httpStore{baseURL: parsed, client: client}, nil
}

func (store *httpStore) Put(ctx context.Context, key Key, value []byte) (err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, store.url(key), bytes.NewReader(value))
	if err != nil {
		return fmt.Errorf("create put request for blob %q: %w", key, err)
	}

	response, err := store.client.Do(request)
	if err != nil {
		return fmt.Errorf("put blob %q: %w", key, err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close put response body for blob %q: %w", key, closeErr)
		}
	}()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return unexpectedStatus("put", key, response)
	}

	return nil
}

func (store *httpStore) Get(ctx context.Context, key Key) (value []byte, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, store.url(key), nil)
	if err != nil {
		return nil, fmt.Errorf("create get request for blob %q: %w", key, err)
	}

	response, err := store.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("get blob %q: %w", key, err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil && err == nil {
			value = nil
			err = fmt.Errorf("close get response body for blob %q: %w", key, closeErr)
		}
	}()

	if response.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("get blob %q: %w", key, ErrNotFound)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, unexpectedStatus("get", key, response)
	}

	value, err = io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read blob %q response body: %w", key, err)
	}

	return value, nil
}

func (store *httpStore) url(key Key) string {
	requestURL := *store.baseURL
	requestURL.Path += "/blobs/" + key.String()
	return requestURL.String()
}

func unexpectedStatus(operation string, key Key, response *http.Response) error {
	snippet, err := io.ReadAll(io.LimitReader(response.Body, responseSnippetLimit))
	if err != nil {
		return fmt.Errorf("%s blob %q: read HTTP %s response body: %w", operation, key, response.Status, err)
	}

	return fmt.Errorf("%s blob %q: unexpected HTTP status %s: %q", operation, key, response.Status, snippet)
}
