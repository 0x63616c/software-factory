package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/0x63616c/software-factory/internal/clients/codexauth"
	"github.com/0x63616c/software-factory/internal/clients/k8s"
	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/config"
	"github.com/0x63616c/software-factory/internal/work"
)

// codexRefresherTimeout bounds one HTTP presentation of the refresh token to
// the provider. NewHTTPRefresher refuses a client with no timeout: an
// unbounded presentation would invalidate the lease-expiry reasoning
// codexauth's takeover policy depends on.
const codexRefresherTimeout = 20 * time.Second

// newCodexAuthSource builds the durable credential source used by the main
// worker's direct Responses client. The credential never reaches a sandbox.
//
// It reads cfg.RunWorkerNamespace and cfg.CodexAuthSecretName rather than a new
// environment variable: the worker's Role is already pinned to exactly that
// Secret name by `resourceNames` (infra/src/software-factory.ts), so a second
// spelling here would be either redundant or, if it drifted, a grant that
// covers nothing.
//
// The codex-auth Secret does not exist until it is seeded out of band (#344,
// docs/runbooks/software-factory-seed-codex-auth.md). Its absence surfaces
// only once a model turn asks the source for a credential — this
// function only builds the client, deliberately: failing worker boot over a
// Secret a human has not created yet would crashloop a worker that has
// nothing to do until its first ticket anyway.
func newCodexAuthSource(cfg config.Worker, clk clock.Clock, logger *slog.Logger) (*codexauth.Source, error) {
	api, err := k8s.NewInClusterAPI()
	if err != nil {
		return nil, fmt.Errorf("connecting to Kubernetes for the codex credential secret: %w", err)
	}
	store, err := k8s.NewSecretClient(api, cfg.RunWorkerNamespace, cfg.CodexAuthSecretName, logger)
	if err != nil {
		return nil, fmt.Errorf("building the codex credential secret client: %w", err)
	}

	refresher, err := codexauth.NewHTTPRefresher(
		&http.Client{Timeout: codexRefresherTimeout},
		codexauth.DefaultTokenURL, codexauth.DefaultClientID,
	)
	if err != nil {
		return nil, fmt.Errorf("building the codex token refresher: %w", err)
	}

	holder, err := holderID(cfg.PodName)
	if err != nil {
		return nil, fmt.Errorf("building this process's codex credential lease holder identity: %w", err)
	}

	source, err := codexauth.New(store, refresher, clk, logger, holder, work.MaxStageDuration)
	if err != nil {
		return nil, fmt.Errorf("building the codex token source: %w", err)
	}
	return source, nil
}

// holderID names this process for codexauth's refresh lease, in the shape
// Source.New's own doc comment asks the composition root to build:
// "<pod name>/<short random>". The suffix disambiguates a crashed pod's own
// restart, which reuses the same PodName, from the process that is still
// alive and still might hold the lease.
//
// This is the second and last place in the service that names a source of
// randomness — see newPromptRenderer's doc comment for the first and for why
// that matters (workflow code may import neither; both live in cmd/).
func holderID(podName string) (string, error) {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("reading randomness for a lease holder suffix: %w", err)
	}
	return fmt.Sprintf("%s/%s", podName, hex.EncodeToString(suffix)), nil
}
