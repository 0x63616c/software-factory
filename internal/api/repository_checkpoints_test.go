package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/checkpoint"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

type repositoryCheckpointStoreFake struct {
	loaded store.GitCheckpoint
	found  bool
	input  store.RepositoryCheckpointInput
	err    error
}

func (f *repositoryCheckpointStoreFake) LoadRepositoryCheckpoint(_ context.Context, _ work.RunWorkerIdentity, _ string) (store.GitCheckpoint, bool, error) {
	return f.loaded, f.found, f.err
}

func (f *repositoryCheckpointStoreFake) CheckpointRepository(_ context.Context, input store.RepositoryCheckpointInput) (store.GitCheckpoint, error) {
	f.input = input
	return input.GitCheckpoint, f.err
}

func (f *repositoryCheckpointStoreFake) CheckpointRepositoryEffect(_ context.Context, input store.RepositoryCheckpointInput) (store.GitCheckpoint, error) {
	f.input = input
	return input.GitCheckpoint, f.err
}

func TestRepositoryCheckpointRoundTripsGenerationScopedEvidence(t *testing.T) {
	const runID = "0f466627-b3ae-4ba2-9c96-6ef44ec6f578"
	completedAt := time.Date(2026, 7, 31, 20, 0, 0, 0, time.UTC)
	fake := &repositoryCheckpointStoreFake{found: true, loaded: store.GitCheckpoint{
		RunID: runID, StepOrdinal: 3, Branch: "factory/run", PushedHead: "head-3", StepResult: json.RawMessage(`{"kind":"synced"}`),
	}}
	service := NewWithRunWorkerStores("test", nil, nil, fake)

	body, err := json.Marshal(checkpoint.RepositoryWrite{Repository: checkpoint.Repository{
		StepOrdinal: 3, Branch: "factory/run", PushedHead: "head-3", StepResult: json.RawMessage(`{"kind":"synced"}`),
	}, CompletedAt: completedAt})
	if err != nil {
		t.Fatal(err)
	}
	path := checkpoint.RepositoryPathFor(runID, 2)
	request := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(checkpoint.RepositoryCapabilityHeader, "repository-capability")
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT repository checkpoint = %d: %s", response.Code, response.Body.String())
	}
	if fake.input.Identity.Generation != 2 || fake.input.Identity.RunID != runID || fake.input.Capability != "repository-capability" || !fake.input.CompletedAt.Equal(completedAt) || fake.input.GitCheckpoint.PushedHead != "head-3" {
		t.Fatalf("checkpoint input = %+v", fake.input)
	}
	if bytes.Contains(response.Body.Bytes(), []byte("repository-capability")) {
		t.Fatal("repository checkpoint response leaked its capability")
	}

	request = httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set(checkpoint.RepositoryCapabilityHeader, "repository-capability")
	response = httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("head-3")) {
		t.Fatalf("GET repository checkpoint = %d: %s", response.Code, response.Body.String())
	}
}

func TestRepositoryCheckpointReportsMissingAndFencedCapabilities(t *testing.T) {
	const path = "/v1/run-worker/runs/0f466627-b3ae-4ba2-9c96-6ef44ec6f578/generations/1/repository-checkpoint"
	for _, test := range []struct {
		name       string
		capability string
		fake       *repositoryCheckpointStoreFake
		wantStatus int
	}{
		{name: "missing", capability: "cap", fake: &repositoryCheckpointStoreFake{}, wantStatus: http.StatusNoContent},
		{name: "no capability", fake: &repositoryCheckpointStoreFake{}, wantStatus: http.StatusUnauthorized},
		{name: "stale generation", capability: "cap", fake: &repositoryCheckpointStoreFake{err: store.ErrRunOwnership}, wantStatus: http.StatusUnauthorized},
		{name: "conflict", capability: "cap", fake: &repositoryCheckpointStoreFake{err: work.ErrPermanent}, wantStatus: http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set(checkpoint.RepositoryCapabilityHeader, test.capability)
			response := httptest.NewRecorder()
			NewWithRunWorkerStores("test", nil, nil, test.fake).Handler().ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d body %s, want %d", response.Code, response.Body.String(), test.wantStatus)
			}
		})
	}
}
