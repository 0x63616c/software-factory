//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/0x63616c/software-factory/internal/activities"
	agentactivities "github.com/0x63616c/software-factory/internal/activities/agent"
	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/agenttools"
	factoryapi "github.com/0x63616c/software-factory/internal/api"
	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/clients/codexresponses"
	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/database/databasetest"
	"github.com/0x63616c/software-factory/internal/prompts"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/0x63616c/software-factory/internal/workflows"
)

type e2eResult struct {
	TicketState         string   `json:"ticketState"`
	RunOutcome          string   `json:"runOutcome"`
	AgentWorkflowStages []string `json:"agentWorkflowStages"`
	Merge               struct {
		Method              string `json:"method"`
		ReviewedHeadMatched bool   `json:"reviewedHeadMatched"`
	} `json:"merge"`
	ActiveRuns          int    `json:"activeRuns"`
	RemainingRunWorkers int    `json:"remainingRunWorkers"`
	ModelAdapter        string `json:"modelAdapter"`
	GitHubAdapter       string `json:"githubAdapter"`
}

type e2eTicket struct {
	ID    int64  `json:"id"`
	State string `json:"state"`
}

type e2eRuns struct {
	Runs []struct {
		Outcome string `json:"outcome"`
		Active  bool   `json:"active"`
		Merge   *struct {
			ReviewedHead string `json:"reviewedHead"`
			MergeSHA     string `json:"mergeSha"`
		} `json:"confirmedMerge"`
		Steps []struct {
			Kind     string `json:"kind"`
			Attempts []struct {
				AgentStage string `json:"agentStage"`
			} `json:"attempts"`
		} `json:"steps"`
	} `json:"runs"`
}

func runE2E(t *testing.T) e2eResult {
	t.Helper()
	ctx := t.Context()
	factoryStore := store.New(databasetest.NewPool(t))
	apiServer := httptest.NewServer(factoryapi.New("e2e", nil, factoryStore).Handler())
	t.Cleanup(apiServer.Close)
	ticket := createTicket(t, apiServer.URL)

	temporal := startTemporal(t)
	blobStore := blobs.NewMemStore()
	responses := newFakeResponses(t)
	runWorkers := newFakeRunWorkers(t, temporal.Client(), factoryStore)
	startFactoryWorkers(t, temporal.Client(), factoryStore, blobStore, responses.client, runWorkers)

	dispatcher, err := temporal.Client().ExecuteWorkflow(ctx, temporalclient.StartWorkflowOptions{
		ID: "software-factory-e2e-dispatcher", TaskQueue: work.TargetDispatcherTaskQueue,
	}, workflows.Dispatcher, workflows.DispatcherInput{
		Policy: work.DefaultDispatcherPolicy(), CloneURL: "https://github.com/example/e2e.git",
		Model: work.DefaultFactoryConfig().DefaultModel,
	})
	if err != nil {
		t.Fatalf("start Dispatcher: %v", err)
	}
	t.Cleanup(func() {
		_ = temporal.Client().CancelWorkflow(context.Background(), dispatcher.GetID(), dispatcher.GetRunID())
	})

	ticket = waitForTerminalTicket(t, apiServer.URL, ticket.ID)
	runs := readRuns(t, apiServer.URL, ticket.ID)
	waitForRunWorkerCleanup(t, runWorkers)
	result := resultFrom(ticket, runs, runWorkers.remaining())
	writeResult(t, result)
	return result
}

func createTicket(t *testing.T, baseURL string) e2eTicket {
	t.Helper()
	response, err := http.Post(baseURL+"/v1/tickets", "application/json", strings.NewReader(`{"title":"deterministic e2e","body":"prove the durable AgentWorkflow path"}`))
	if err != nil {
		t.Fatalf("create Ticket through API: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("create Ticket status = %d: %s", response.StatusCode, body)
	}
	var ticket e2eTicket
	if err := json.NewDecoder(response.Body).Decode(&ticket); err != nil {
		t.Fatalf("decode created Ticket: %v", err)
	}
	return ticket
}

func waitForTerminalTicket(t *testing.T, baseURL string, ticketID int64) e2eTicket {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(fmt.Sprintf("%s/v1/tickets/%d", baseURL, ticketID))
		if err != nil {
			t.Fatalf("read Ticket through API: %v", err)
		}
		var ticket e2eTicket
		decodeErr := json.NewDecoder(response.Body).Decode(&ticket)
		response.Body.Close()
		if response.StatusCode == http.StatusOK && decodeErr == nil {
			if ticket.State == "done" || ticket.State == "failed" {
				return ticket
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Ticket %d did not become terminal", ticketID)
	return e2eTicket{}
}

func readRuns(t *testing.T, baseURL string, ticketID int64) e2eRuns {
	t.Helper()
	response, err := http.Get(fmt.Sprintf("%s/v1/tickets/%d/runs", baseURL, ticketID))
	if err != nil {
		t.Fatalf("read Runs through API: %v", err)
	}
	defer response.Body.Close()
	var runs e2eRuns
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("read Runs status = %d: %s", response.StatusCode, body)
	}
	if err := json.NewDecoder(response.Body).Decode(&runs); err != nil {
		t.Fatalf("decode Runs: %v", err)
	}
	return runs
}

func resultFrom(ticket e2eTicket, runs e2eRuns, remainingWorkers int) e2eResult {
	result := e2eResult{TicketState: ticket.State, RemainingRunWorkers: remainingWorkers, ModelAdapter: "fake-responses", GitHubAdapter: "fake"}
	result.Merge.Method = "squash"
	if len(runs.Runs) == 0 {
		return result
	}
	run := runs.Runs[0]
	result.RunOutcome = run.Outcome
	if run.Active {
		result.ActiveRuns = 1
	}
	if run.Merge != nil {
		result.Merge.ReviewedHeadMatched = run.Merge.ReviewedHead == "candidate-head" && run.Merge.MergeSHA == "merge-head"
	}
	for _, step := range run.Steps {
		for _, attempt := range step.Attempts {
			result.AgentWorkflowStages = append(result.AgentWorkflowStages, attempt.AgentStage)
		}
	}
	return result
}

func writeResult(t *testing.T, result e2eResult) {
	t.Helper()
	path := os.Getenv("SOFTWARE_FACTORY_E2E_RESULT")
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create E2E artifact directory: %v", err)
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("encode E2E result: %v", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatalf("write E2E result: %v", err)
	}
}

func startTemporal(t *testing.T) *testsuite.DevServer {
	t.Helper()
	version, err := os.ReadFile(filepath.Join("..", "runworkercapability", "temporal-cli-version.txt"))
	if err != nil {
		t.Fatalf("read Temporal CLI version: %v", err)
	}
	cacheDir := filepath.Join(os.TempDir(), "software-factory-temporal-cli")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatalf("create Temporal cache: %v", err)
	}
	server, err := testsuite.StartDevServer(context.Background(), testsuite.DevServerOptions{
		CachedDownload: testsuite.CachedDownload{Version: strings.TrimSpace(string(version)), DestDir: cacheDir},
		LogLevel:       "error",
	})
	if err != nil {
		t.Fatalf("start Temporal dev server: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Stop(); err != nil {
			t.Errorf("stop Temporal dev server: %v", err)
		}
	})
	return server
}

type fakeCredentialSource struct{}

func (fakeCredentialSource) Credential(context.Context) (codexresponses.Credential, error) {
	return codexresponses.Credential{AccessToken: work.NewCredential("e2e-token"), AccountID: "e2e-account"}, nil
}

type fakeResponses struct {
	client *codexresponses.Client
	turns  atomic.Int32
}

func newFakeResponses(t *testing.T) *fakeResponses {
	t.Helper()
	fake := &fakeResponses{}
	server := httptest.NewServer(http.HandlerFunc(fake.serveHTTP))
	t.Cleanup(server.Close)
	client, err := codexresponses.New(&http.Client{Timeout: 5 * time.Second}, server.URL, fakeCredentialSource{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("create Responses client: %v", err)
	}
	fake.client = client
	return fake
}

func (fake *fakeResponses) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Text struct {
			Format struct {
				Name string `json:"name"`
			} `json:"format"`
		} `json:"text"`
	}
	if request.Header.Get("Authorization") != "Bearer e2e-token" || json.NewDecoder(request.Body).Decode(&input) != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	outputs := map[string]string{
		"plan_result":      `{"document":"the plan"}`,
		"implement_result": `{"report":"implemented","blocked":false,"blocked_reason":"","title":"e2e change","body":"deterministic evidence"}`,
		"review_result":    `{"document":"approved","findings":[],"verified":["durable AgentWorkflow path"]}`,
	}
	text, ok := outputs[input.Text.Format.Name]
	if !ok {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	turn := fake.turns.Add(1)
	encodedText, _ := json.Marshal(text)
	writer.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(writer, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_%d\"}}\n\n", turn)
	_, _ = fmt.Fprintf(writer, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":%s}]}}\n\n", encodedText)
	_, _ = fmt.Fprintf(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_%d\",\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n", turn)
}

func startFactoryWorkers(
	t *testing.T,
	client temporalclient.Client,
	factoryStore *store.Store,
	blobStore blobs.Store,
	turner agentactivities.Turner,
	runWorkers *fakeRunWorkers,
) {
	t.Helper()
	ticketActivities, err := activities.NewTicketActivities(factoryStore)
	if err != nil {
		t.Fatalf("create Ticket activities: %v", err)
	}
	recording, err := activities.NewTargetRecordingActivities(factoryStore)
	if err != nil {
		t.Fatalf("create recording activities: %v", err)
	}
	recovery, err := activities.NewTargetRecoveryActivities(factoryStore)
	if err != nil {
		t.Fatalf("create recovery activities: %v", err)
	}
	evidence, err := activities.NewTargetAgentEvidenceActivities(factoryStore, blobStore)
	if err != nil {
		t.Fatalf("create evidence activities: %v", err)
	}
	renderer, err := prompts.New(bytes.NewReader(bytes.Repeat([]byte{0x5a}, 1024)))
	if err != nil {
		t.Fatalf("create prompt renderer: %v", err)
	}
	promptActivities, err := agentactivities.NewPromptActivities(prompts.NewActivityRenderer(renderer), blobStore)
	if err != nil {
		t.Fatalf("create prompt activities: %v", err)
	}
	toolsets, err := agenttools.NewToolsets(work.RepoDir, "e2e/catalog", blobStore)
	if err != nil {
		t.Fatalf("create toolsets: %v", err)
	}
	modelActivities, err := agentactivities.NewActivities(turner, blobStore, clock.System{}, toolsets...)
	if err != nil {
		t.Fatalf("create model activities: %v", err)
	}

	controlWorker := worker.New(client, work.TargetDispatcherTaskQueue, worker.Options{})
	controlWorker.RegisterWorkflow(workflows.Dispatcher)
	controlWorker.RegisterActivity(ticketActivities.AwaitDispatchableTickets)
	startWorker(t, controlWorker)

	mainWorker := worker.New(client, work.TaskQueue, worker.Options{})
	mainWorker.RegisterWorkflow(workflows.WorkOnTicket)
	mainWorker.RegisterWorkflowWithOptions(workflows.AgentWorkflow, workflow.RegisterOptions{Name: agent.WorkflowName})
	mainWorker.RegisterActivity(recording)
	mainWorker.RegisterActivity(recovery)
	mainWorker.RegisterActivityWithOptions(evidence.Finalize, activity.RegisterOptions{Name: activities.TargetAgentEvidenceFinalizeActivityName})
	mainWorker.RegisterActivityWithOptions(promptActivities.Prepare, activity.RegisterOptions{Name: agent.PrepareActivityName})
	mainWorker.RegisterActivityWithOptions(modelActivities.ModelTurn, activity.RegisterOptions{Name: agent.ModelTurnActivityName})
	mainWorker.RegisterActivityWithOptions(modelActivities.RecordLifecycle, activity.RegisterOptions{Name: agent.LifecycleActivityName})
	mainWorker.RegisterActivityWithOptions(promptActivities.DecodeFinalOutput, activity.RegisterOptions{Name: agent.FinalizeActivityName})
	runWorkers.registerMain(mainWorker)
	startWorker(t, mainWorker)
}

func startWorker(t *testing.T, temporalWorker worker.Worker) {
	t.Helper()
	if err := temporalWorker.Start(); err != nil {
		t.Fatalf("start Temporal worker: %v", err)
	}
	t.Cleanup(temporalWorker.Stop)
}

func waitForRunWorkerCleanup(t *testing.T, runWorkers *fakeRunWorkers) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runWorkers.remaining() == 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("Run Workers remaining after terminal workflow = %d", runWorkers.remaining())
}
