//go:build integration

package runworkercapability

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/0x63616c/software-factory/internal/activities"
	temporalclient "github.com/0x63616c/software-factory/internal/clients/temporal"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

//go:embed temporal-cli-version.txt
var temporalCLIVersion string

const (
	mainQueue       = "capability-main"
	privateQueueOne = "capability-private-one"
	privateQueueTwo = "capability-private-two"

	cloneActivityName       = "run-worker-clone"
	agentActivityName       = "run-worker-agent"
	ciActivityName          = "run-worker-ci"
	pullRequestActivityName = "run-worker-pull-request"
	mergeActivityName       = "run-worker-merge"
	reconcileActivityName   = "run-worker-reconcile-incomplete-attempt"
	restoreActivityName     = "run-worker-restore-checkpoint"
	controlActivityName     = "capability-control-activity"
	recordingActivityName   = "main-control-recording"
	rotationActivityName    = "main-control-credential-rotation"
)

type repositoryActivityDefinition struct {
	Operation string
	Name      string
}

var targetRepositoryActivities = [...]repositoryActivityDefinition{
	{Operation: "clone", Name: cloneActivityName},
	{Operation: "agent", Name: agentActivityName},
	{Operation: "ci", Name: ciActivityName},
	{Operation: "pull_request", Name: pullRequestActivityName},
	{Operation: "merge", Name: mergeActivityName},
}

var replacementActivities = [...]repositoryActivityDefinition{
	{Operation: "reconcile_attempt", Name: reconcileActivityName},
	{Operation: "restore", Name: restoreActivityName},
}

var (
	privateHelperMode     = flag.Bool("capability-private-worker-helper", false, "run as a private worker helper")
	privateHelperTemporal = flag.String("capability-temporal-host-port", "", "Temporal frontend for the helper")
	privateHelperQueue    = flag.String("capability-private-queue", "", "private queue for the helper")
	privateHelperIdentity = flag.String("capability-private-identity", "", "private worker identity")
	privateHelperRoot     = flag.String("capability-private-root", "", "isolated filesystem root")
)

type sessionActivityInput struct {
	Operation  string
	MarkerName string
	Marker     string
	Write      bool
}

type sessionActivityEvidence struct {
	Operation string
	Worker    string
	ProcessID int
	Found     bool
	Marker    string
	Recovery  attemptRecoveryOutcome
}

type attemptRecoveryOutcome string

const attemptRecoveryUnresumable attemptRecoveryOutcome = "unresumable_incomplete_attempt"

type sessionWorkflowInput struct {
	PrivateQueue string
	OtherQueue   string
	MarkerName   string
	Marker       string
}

type sessionEvidence struct {
	Repository []sessionActivityEvidence
	First      sessionActivityEvidence
	Second     sessionActivityEvidence
	OtherRoot  sessionActivityEvidence
	Control    string
}

type sessionLossInput struct {
	FirstQueue        string
	ReplacementQueue  string
	MarkerName        string
	FirstMarker       string
	ReplacementMarker string
}

type sessionLossEvidence struct {
	First             sessionActivityEvidence
	Failure           string
	Control           string
	Recording         string
	Rotation          string
	Recovery          attemptRecoveryOutcome
	Completed         []sessionActivityEvidence
	ReplacementBefore sessionActivityEvidence
	Replacement       sessionActivityEvidence
}

type productionSessionInput struct {
	PrivateQueue string
	Branch       string
}

const productionEvidenceFilename = "registered-run-worker-evidence.json"

var productionRunWorkerIdentity = work.RunWorkerIdentity{
	RunID:      "0f466627-b3ae-4ba2-9c96-6ef44ec6f578",
	Generation: 1,
}

type productionEvidence struct {
	Identity   work.RunWorkerIdentity
	ProcessID  int
	Operations []string
}

type privateWorkerProcess struct {
	cmd     *exec.Cmd
	output  bytes.Buffer
	stopped bool
}

func TestSessionPinsRepositoryWorkToOneIsolatedPrivateWorker(t *testing.T) {
	server := startServer(t)
	rootOne := t.TempDir()
	rootTwo := t.TempDir()
	startWorker(t, mainCapabilityWorker(server.Client(), "main"))
	privateOne := startPrivateWorkerProcess(t, server.FrontendHostPort(), privateQueueOne, "private-one", rootOne)
	privateTwo := startPrivateWorkerProcess(t, server.FrontendHostPort(), privateQueueTwo, "private-two", rootTwo)

	run, err := server.Client().ExecuteWorkflow(context.Background(), temporalclient.StartWorkflowOptions{
		ID:        "session-capability-affinity",
		TaskQueue: mainQueue,
	}, sessionEvidenceWorkflow, sessionWorkflowInput{
		PrivateQueue: privateQueueOne,
		OtherQueue:   privateQueueTwo,
		MarkerName:   "repository.marker",
		Marker:       "repository-state-v1",
	})
	if err != nil {
		t.Fatalf("starting workflow: %v", err)
	}

	var evidence sessionEvidence
	if err := run.Get(context.Background(), &evidence); err != nil {
		t.Fatalf("getting workflow result: %v", err)
	}
	if evidence.First.Worker != "private-one" || evidence.Second.Worker != "private-one" ||
		evidence.First.ProcessID != privateOne.processID() || evidence.Second.ProcessID != privateOne.processID() {
		t.Fatalf("Session activity routing = %#v, want both in private-one process %d", evidence, privateOne.processID())
	}
	if !evidence.First.Found || !evidence.Second.Found ||
		evidence.First.Marker != "repository-state-v1" || evidence.Second.Marker != "repository-state-v1" {
		t.Fatalf("private-one filesystem marker evidence = %#v", evidence)
	}
	operations := []string{evidence.First.Operation, evidence.Second.Operation}
	if !slices.Equal(operations, []string{"clone", "agent"}) {
		t.Fatalf("repository operation order = %v, want clone then agent", operations)
	}
	targetOperations := make([]string, 0, len(evidence.Repository))
	for _, operation := range evidence.Repository {
		targetOperations = append(targetOperations, operation.Operation)
	}
	if !slices.Equal(targetOperations, []string{"clone", "agent", "ci", "pull_request", "merge"}) {
		t.Fatalf("target repository operations = %v, want the complete target sequence", targetOperations)
	}
	if evidence.OtherRoot.Worker != "private-two" || evidence.OtherRoot.ProcessID != privateTwo.processID() ||
		evidence.OtherRoot.Found {
		t.Fatalf("private-two isolation probe = %#v, want marker absent in process %d", evidence.OtherRoot, privateTwo.processID())
	}
	if evidence.Control != "main-control" {
		t.Fatalf("main-control activity = %q, want main-control", evidence.Control)
	}
}

func TestSessionRunsTheRegisteredRunWorkerActivitiesOnItsPrivateWorker(t *testing.T) {
	server := startServer(t)
	rootOne := t.TempDir()
	rootTwo := t.TempDir()
	startWorker(t, mainCapabilityWorker(server.Client(), "main"))
	privateOne := startPrivateWorkerProcess(t, server.FrontendHostPort(), privateQueueOne, "private-one", rootOne)
	startPrivateWorkerProcess(t, server.FrontendHostPort(), privateQueueTwo, "private-two", rootTwo)

	run, err := server.Client().ExecuteWorkflow(context.Background(), temporalclient.StartWorkflowOptions{
		ID:        "session-registered-run-worker-activities",
		TaskQueue: mainQueue,
	}, productionSessionWorkflow, productionSessionInput{
		PrivateQueue: privateQueueOne,
		Branch:       work.FactoryTicketBranchName(42, productionRunWorkerIdentity.RunID),
	})
	if err != nil {
		t.Fatalf("starting workflow: %v", err)
	}

	if err := run.Get(context.Background(), nil); err != nil {
		t.Fatalf("getting workflow result: %v", err)
	}
	evidence := readProductionEvidence(t, rootOne)
	if evidence.ProcessID != privateOne.processID() || evidence.Identity != productionRunWorkerIdentity {
		t.Fatalf("Run Worker evidence = %#v, want identity %#v in process %d", evidence, productionRunWorkerIdentity, privateOne.processID())
	}
	if !slices.Equal(evidence.Operations, []string{"clone", "ci", "publish", "sync", "ready", "merge"}) {
		t.Fatalf("Run Worker operations = %v", evidence.Operations)
	}
	if _, err := os.Stat(filepath.Join(rootTwo, productionEvidenceFilename)); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("other private worker target state error = %v, want absent", err)
	}
}

func TestSessionCreationWaitsForRunWorkerReadiness(t *testing.T) {
	server := startServer(t)
	root := t.TempDir()
	startWorker(t, mainCapabilityWorker(server.Client(), "main"))

	run, err := server.Client().ExecuteWorkflow(context.Background(), temporalclient.StartWorkflowOptions{
		ID:        "session-target-readiness",
		TaskQueue: mainQueue,
	}, sessionReadinessWorkflow, sessionWorkflowInput{
		PrivateQueue: privateQueueOne,
		MarkerName:   "repository.marker",
		Marker:       "repository-state-v1",
	})
	if err != nil {
		t.Fatalf("starting workflow: %v", err)
	}

	blocked, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	var deadline *serviceerror.DeadlineExceeded
	if err := run.Get(blocked, nil); !errors.Is(err, context.DeadlineExceeded) && !errors.As(err, &deadline) {
		t.Fatalf("workflow before Run Worker readiness = %T: %v, want a deadline error", err, err)
	}

	private := startPrivateWorkerProcess(t, server.FrontendHostPort(), privateQueueOne, "private-ready", root)
	var evidence sessionActivityEvidence
	if err := run.Get(context.Background(), &evidence); err != nil {
		t.Fatalf("getting workflow result after Run Worker readiness: %v", err)
	}
	if evidence.Operation != "clone" || evidence.Worker != "private-ready" ||
		evidence.ProcessID != private.processID() || !evidence.Found || evidence.Marker != "repository-state-v1" {
		t.Fatalf("first repository activity after readiness = %#v", evidence)
	}
}

func TestSessionStaysOnItsPrivateProcessAcrossAMainWorkerRestart(t *testing.T) {
	server := startServer(t)
	rootOne := t.TempDir()
	mainWorker := mainCapabilityWorker(server.Client(), "main-initial")
	if err := mainWorker.Start(); err != nil {
		t.Fatalf("starting initial main worker: %v", err)
	}
	privateOne := startPrivateWorkerProcess(t, server.FrontendHostPort(), privateQueueOne, "private-one", rootOne)

	run, err := server.Client().ExecuteWorkflow(context.Background(), temporalclient.StartWorkflowOptions{
		ID:        "session-capability-main-restart",
		TaskQueue: mainQueue,
	}, sessionRestartWorkflow, sessionWorkflowInput{
		PrivateQueue: privateQueueOne,
		MarkerName:   "repository.marker",
		Marker:       "repository-state-v1",
	})
	if err != nil {
		t.Fatalf("starting workflow: %v", err)
	}
	waitForFile(t, filepath.Join(rootOne, "repository.marker"), "first private activity marker")

	mainWorker.Stop()
	startWorker(t, mainCapabilityWorker(server.Client(), "main-replacement"))
	if err := server.Client().SignalWorkflow(context.Background(), run.GetID(), run.GetRunID(), "continue", "resume"); err != nil {
		t.Fatalf("signalling workflow after main-worker restart: %v", err)
	}

	var evidence sessionEvidence
	if err := run.Get(context.Background(), &evidence); err != nil {
		t.Fatalf("getting workflow result: %v", err)
	}
	if evidence.First.Worker != "private-one" || evidence.Second.Worker != "private-one" ||
		evidence.First.ProcessID != privateOne.processID() || evidence.Second.ProcessID != privateOne.processID() ||
		evidence.First.Marker != "repository-state-v1" || evidence.Second.Marker != "repository-state-v1" ||
		evidence.Control != "main-replacement-control" {
		t.Fatalf("evidence after main-worker restart = %#v", evidence)
	}
	operations := make([]string, 0, len(evidence.Repository))
	for _, operation := range evidence.Repository {
		operations = append(operations, operation.Operation)
	}
	if !slices.Equal(operations, []string{"clone", "agent", "ci", "pull_request", "merge"}) {
		t.Fatalf("repository operations across main-worker restart = %v", operations)
	}
}

func TestSessionLossLeavesMainControlAndRoutesAReplacementToItsOwnRoot(t *testing.T) {
	server := startServer(t)
	rootOne := t.TempDir()
	rootTwo := t.TempDir()
	startWorker(t, mainCapabilityWorker(server.Client(), "main"))
	privateOne := startPrivateWorkerProcess(t, server.FrontendHostPort(), privateQueueOne, "private-one", rootOne)

	run, err := server.Client().ExecuteWorkflow(context.Background(), temporalclient.StartWorkflowOptions{
		ID:        "session-capability-worker-loss",
		TaskQueue: mainQueue,
	}, sessionLossWorkflow, sessionLossInput{
		FirstQueue:        privateQueueOne,
		ReplacementQueue:  privateQueueTwo,
		MarkerName:        "repository.marker",
		FirstMarker:       "repository-state-v1",
		ReplacementMarker: "replacement-state-v1",
	})
	if err != nil {
		t.Fatalf("starting workflow: %v", err)
	}
	waitForFile(t, filepath.Join(rootOne, "repository.marker"), "first private activity marker")

	privateOne.stop(t)
	privateTwo := startPrivateWorkerProcess(t, server.FrontendHostPort(), privateQueueTwo, "private-replacement", rootTwo)
	if err := server.Client().SignalWorkflow(context.Background(), run.GetID(), run.GetRunID(), "continue", "resume"); err != nil {
		t.Fatalf("signalling workflow after private-worker loss: %v", err)
	}

	var evidence sessionLossEvidence
	if err := run.Get(context.Background(), &evidence); err != nil {
		t.Fatalf("getting workflow result: %v", err)
	}
	if evidence.First.Worker != "private-one" || evidence.First.ProcessID != privateOne.processID() ||
		evidence.First.Marker != "repository-state-v1" {
		t.Fatalf("initial Session evidence = %#v", evidence.First)
	}
	if evidence.Failure == "" || evidence.Control != "main-control" {
		t.Fatalf("Session-loss control evidence = %#v", evidence)
	}
	if evidence.Recording != "main-recording" || evidence.Rotation != "main-rotation" {
		t.Fatalf("main-control recovery calls = %#v", evidence)
	}
	if evidence.Recovery != attemptRecoveryUnresumable {
		t.Fatalf("incomplete Attempt recovery = %q, want %q", evidence.Recovery, attemptRecoveryUnresumable)
	}
	if len(evidence.Completed) != 1 || evidence.Completed[0].Operation != "clone" {
		t.Fatalf("completed repository Steps = %#v, want clone exactly once", evidence.Completed)
	}
	if evidence.ReplacementBefore.Worker != "private-replacement" ||
		evidence.ReplacementBefore.ProcessID != privateTwo.processID() || evidence.ReplacementBefore.Found ||
		evidence.ReplacementBefore.Operation != "reconcile_attempt" ||
		evidence.ReplacementBefore.Recovery != attemptRecoveryUnresumable {
		t.Fatalf("replacement pre-write isolation evidence = %#v", evidence.ReplacementBefore)
	}
	if evidence.Replacement.Worker != "private-replacement" ||
		evidence.Replacement.ProcessID != privateTwo.processID() || !evidence.Replacement.Found ||
		evidence.Replacement.Marker != "replacement-state-v1" || evidence.Replacement.Operation != "restore" {
		t.Fatalf("replacement routing evidence = %#v", evidence.Replacement)
	}
	allOperations := []string{evidence.First.Operation, evidence.ReplacementBefore.Operation, evidence.Replacement.Operation}
	if !slices.Equal(allOperations, []string{"clone", "reconcile_attempt", "restore"}) {
		t.Fatalf("repository operations after replacement = %v, want no rerun and no fresh agent execution", allOperations)
	}
}

func TestPrivateWorkerHelperProcess(t *testing.T) {
	if !*privateHelperMode {
		return
	}
	hostPort := *privateHelperTemporal
	queue := *privateHelperQueue
	identity := *privateHelperIdentity
	root := *privateHelperRoot
	if hostPort == "" || queue == "" || identity == "" || root == "" {
		t.Fatal("private worker helper environment is incomplete")
	}

	c, err := temporalclient.Dial(temporalclient.Options{HostPort: hostPort}, nil, nil)
	if err != nil {
		t.Fatalf("dialling Temporal: %v", err)
	}
	defer c.Close()
	w, err := privateWorker(c, queue, identity, root)
	if err != nil {
		t.Fatalf("composing private worker: %v", err)
	}
	if err := w.Start(); err != nil {
		t.Fatalf("starting private worker: %v", err)
	}
	defer w.Stop()
	if err := os.WriteFile(filepath.Join(root, ".worker-ready"), []byte("ready"), 0o600); err != nil {
		t.Fatalf("writing helper ready marker: %v", err)
	}

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupt)
	<-interrupt
}

func sessionEvidenceWorkflow(ctx workflow.Context, in sessionWorkflowInput) (sessionEvidence, error) {
	sessionCtx, err := createCapabilitySession(ctx, in.PrivateQueue)
	if err != nil {
		return sessionEvidence{}, fmt.Errorf("creating session: %w", err)
	}
	defer workflow.CompleteSession(sessionCtx)

	result := sessionEvidence{Repository: make([]sessionActivityEvidence, 0, len(targetRepositoryActivities))}
	for index, definition := range targetRepositoryActivities {
		input := sessionActivityInput{MarkerName: in.MarkerName}
		if index == 0 {
			input.Marker = in.Marker
			input.Write = true
		}
		var evidence sessionActivityEvidence
		if err := workflow.ExecuteActivity(sessionCtx, definition.Name, input).Get(sessionCtx, &evidence); err != nil {
			return sessionEvidence{}, fmt.Errorf("running %s repository activity: %w", definition.Operation, err)
		}
		result.Repository = append(result.Repository, evidence)
	}
	result.First = result.Repository[0]
	result.Second = result.Repository[1]
	second := sessionActivityInput{MarkerName: in.MarkerName}
	otherCtx := privateActivityContext(ctx, in.OtherQueue)
	if err := workflow.ExecuteActivity(otherCtx, ciActivityName, second).Get(otherCtx, &result.OtherRoot); err != nil {
		return sessionEvidence{}, fmt.Errorf("probing other private root: %w", err)
	}
	controlCtx := mainControlActivityContext(ctx)
	if err := workflow.ExecuteActivity(controlCtx, controlActivityName).Get(controlCtx, &result.Control); err != nil {
		return sessionEvidence{}, fmt.Errorf("running main-control activity: %w", err)
	}
	return result, nil
}

func productionSessionWorkflow(ctx workflow.Context, in productionSessionInput) error {
	sessionCtx, err := createCapabilitySession(ctx, in.PrivateQueue)
	if err != nil {
		return fmt.Errorf("creating production-shaped session: %w", err)
	}
	defer workflow.CompleteSession(sessionCtx)

	cloneStep := activities.RepositoryStep{StepOrdinal: 1, Branch: in.Branch, ObservedBase: "base-head"}
	var clone activities.CloneTargetRepositoryOutput
	if err := workflow.ExecuteActivity(sessionCtx, "CloneTargetRepository", activities.CloneTargetRepositoryInput{
		Step: cloneStep, CloneURL: "https://github.com/example/repo.git",
	}).Get(sessionCtx, &clone); err != nil {
		return fmt.Errorf("running registered clone activity: %w", err)
	}
	ciStep := activities.RepositoryStep{StepOrdinal: 2, Branch: in.Branch, PushedHead: clone.HeadSHA, ObservedBase: "base-head"}
	if err := workflow.ExecuteActivity(sessionCtx, "TargetAwaitCI", activities.TargetAwaitCIInput{
		Step: ciStep,
		CI:   activities.AwaitCIInput{CommitSHA: clone.HeadSHA, RequiredChecks: []string{"test"}},
	}).Get(sessionCtx, nil); err != nil {
		return fmt.Errorf("running registered target CI activity: %w", err)
	}
	prStep := activities.RepositoryStep{StepOrdinal: 3, Branch: in.Branch, PushedHead: clone.HeadSHA, ObservedBase: "base-head"}
	var pullRequest work.PullRequest
	if err := workflow.ExecuteActivity(sessionCtx, "TargetSyncPullRequest", activities.TargetSyncPullRequestInput{
		Step: prStep, Title: "Implement ticket", Body: "Ready",
	}).Get(sessionCtx, &pullRequest); err != nil {
		return fmt.Errorf("running registered target pull request sync activity: %w", err)
	}
	readyStep := activities.RepositoryStep{
		StepOrdinal: 4, Branch: in.Branch, PushedHead: clone.HeadSHA, ObservedBase: "base-head",
		PullRequestNumber: pullRequest.Number, PullRequestNodeID: pullRequest.NodeID,
	}
	if err := workflow.ExecuteActivity(sessionCtx, "TargetMarkPullRequestReady", activities.TargetMarkPullRequestReadyInput{Step: readyStep}).Get(sessionCtx, nil); err != nil {
		return fmt.Errorf("running registered target pull request ready activity: %w", err)
	}
	mergeStep := readyStep
	mergeStep.StepOrdinal = 5
	if err := workflow.ExecuteActivity(sessionCtx, "TargetMergePullRequest", activities.TargetMergePullRequestInput{
		Step: mergeStep, ExpectedHeadSHA: clone.HeadSHA,
	}).Get(sessionCtx, nil); err != nil {
		return fmt.Errorf("running registered target pull request merge activity: %w", err)
	}
	return nil
}

func sessionReadinessWorkflow(ctx workflow.Context, in sessionWorkflowInput) (sessionActivityEvidence, error) {
	sessionCtx, err := createCapabilitySession(ctx, in.PrivateQueue)
	if err != nil {
		return sessionActivityEvidence{}, fmt.Errorf("creating session: %w", err)
	}
	defer workflow.CompleteSession(sessionCtx)

	input := sessionActivityInput{MarkerName: in.MarkerName, Marker: in.Marker, Write: true}
	var evidence sessionActivityEvidence
	if err := workflow.ExecuteActivity(sessionCtx, cloneActivityName, input).Get(sessionCtx, &evidence); err != nil {
		return sessionActivityEvidence{}, fmt.Errorf("running first repository activity: %w", err)
	}
	return evidence, nil
}

func sessionRestartWorkflow(ctx workflow.Context, in sessionWorkflowInput) (sessionEvidence, error) {
	sessionCtx, err := createCapabilitySession(ctx, in.PrivateQueue)
	if err != nil {
		return sessionEvidence{}, fmt.Errorf("creating session: %w", err)
	}
	defer workflow.CompleteSession(sessionCtx)

	result := sessionEvidence{Repository: make([]sessionActivityEvidence, 0, len(targetRepositoryActivities))}
	first := sessionActivityInput{MarkerName: in.MarkerName, Marker: in.Marker, Write: true}
	var firstEvidence sessionActivityEvidence
	if err := workflow.ExecuteActivity(sessionCtx, cloneActivityName, first).Get(sessionCtx, &firstEvidence); err != nil {
		return sessionEvidence{}, fmt.Errorf("running first private activity: %w", err)
	}
	result.Repository = append(result.Repository, firstEvidence)
	var continueRun string
	workflow.GetSignalChannel(ctx, "continue").Receive(ctx, &continueRun)
	for _, definition := range targetRepositoryActivities[1:] {
		var evidence sessionActivityEvidence
		input := sessionActivityInput{MarkerName: in.MarkerName}
		if err := workflow.ExecuteActivity(sessionCtx, definition.Name, input).Get(sessionCtx, &evidence); err != nil {
			return sessionEvidence{}, fmt.Errorf("running %s private activity: %w", definition.Operation, err)
		}
		result.Repository = append(result.Repository, evidence)
	}
	result.First = result.Repository[0]
	result.Second = result.Repository[1]
	controlCtx := mainControlActivityContext(ctx)
	if err := workflow.ExecuteActivity(controlCtx, controlActivityName).Get(controlCtx, &result.Control); err != nil {
		return sessionEvidence{}, fmt.Errorf("running main-control activity: %w", err)
	}
	return result, nil
}

func sessionLossWorkflow(ctx workflow.Context, in sessionLossInput) (sessionLossEvidence, error) {
	sessionCtx, err := createCapabilitySession(ctx, in.FirstQueue)
	if err != nil {
		return sessionLossEvidence{}, fmt.Errorf("creating initial session: %w", err)
	}
	defer workflow.CompleteSession(sessionCtx)

	var result sessionLossEvidence
	first := sessionActivityInput{Operation: "clone", MarkerName: in.MarkerName, Marker: in.FirstMarker, Write: true}
	if err := workflow.ExecuteActivity(sessionCtx, cloneActivityName, first).Get(sessionCtx, &result.First); err != nil {
		return sessionLossEvidence{}, fmt.Errorf("running first private activity: %w", err)
	}
	result.Completed = append(result.Completed, result.First)
	var continueRun string
	workflow.GetSignalChannel(ctx, "continue").Receive(ctx, &continueRun)
	read := sessionActivityInput{Operation: "agent", MarkerName: in.MarkerName}
	if err := workflow.ExecuteActivity(sessionCtx, agentActivityName, read).Get(sessionCtx, nil); err == nil {
		return sessionLossEvidence{}, errors.New("lost Session unexpectedly accepted another activity")
	} else {
		result.Failure = err.Error()
	}
	controlCtx := mainControlActivityContext(ctx)
	if err := workflow.ExecuteActivity(controlCtx, controlActivityName).Get(controlCtx, &result.Control); err != nil {
		return sessionLossEvidence{}, fmt.Errorf("running main-control activity after Session loss: %w", err)
	}
	if err := workflow.ExecuteActivity(controlCtx, recordingActivityName).Get(controlCtx, &result.Recording); err != nil {
		return sessionLossEvidence{}, fmt.Errorf("recording the interrupted Attempt after Session loss: %w", err)
	}
	if err := workflow.ExecuteActivity(controlCtx, rotationActivityName).Get(controlCtx, &result.Rotation); err != nil {
		return sessionLossEvidence{}, fmt.Errorf("rotating credentials after Session loss: %w", err)
	}

	replacementSession, err := createCapabilitySession(ctx, in.ReplacementQueue)
	if err != nil {
		return sessionLossEvidence{}, fmt.Errorf("creating replacement session: %w", err)
	}
	defer workflow.CompleteSession(replacementSession)
	if err := workflow.ExecuteActivity(replacementSession, reconcileActivityName, read).
		Get(replacementSession, &result.ReplacementBefore); err != nil {
		return sessionLossEvidence{}, fmt.Errorf("probing replacement private root: %w", err)
	}
	result.Recovery = result.ReplacementBefore.Recovery
	if result.Recovery != attemptRecoveryUnresumable {
		return sessionLossEvidence{}, fmt.Errorf("replacement returned Attempt recovery %q", result.Recovery)
	}
	replacement := sessionActivityInput{Operation: "restore", MarkerName: in.MarkerName, Marker: in.ReplacementMarker, Write: true}
	if err := workflow.ExecuteActivity(replacementSession, restoreActivityName, replacement).
		Get(replacementSession, &result.Replacement); err != nil {
		return sessionLossEvidence{}, fmt.Errorf("running replacement private activity: %w", err)
	}
	return result, nil
}

func privateActivityContext(ctx workflow.Context, privateQueue string) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           privateQueue,
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	})
}

func mainControlActivityContext(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	})
}

func createCapabilitySession(ctx workflow.Context, privateQueue string) (workflow.Context, error) {
	return workflow.CreateSession(privateActivityContext(ctx, privateQueue), &workflow.SessionOptions{
		ExecutionTimeout: time.Minute,
		CreationTimeout:  time.Minute,
		HeartbeatTimeout: time.Second,
	})
}

func mainCapabilityWorker(c temporalclient.Client, identity string) worker.Worker {
	w := worker.New(c, mainQueue, worker.Options{})
	w.RegisterWorkflow(sessionEvidenceWorkflow)
	w.RegisterWorkflow(sessionReadinessWorkflow)
	w.RegisterWorkflow(sessionRestartWorkflow)
	w.RegisterWorkflow(sessionLossWorkflow)
	w.RegisterWorkflow(productionSessionWorkflow)
	w.RegisterActivityWithOptions(
		func(context.Context) (string, error) { return identity + "-control", nil },
		activity.RegisterOptions{Name: controlActivityName},
	)
	w.RegisterActivityWithOptions(
		func(context.Context) (string, error) { return identity + "-recording", nil },
		activity.RegisterOptions{Name: recordingActivityName},
	)
	w.RegisterActivityWithOptions(
		func(context.Context) (string, error) { return identity + "-rotation", nil },
		activity.RegisterOptions{Name: rotationActivityName},
	)
	return w
}

func privateWorker(c temporalclient.Client, queue, identity, root string) (worker.Worker, error) {
	w := worker.New(c, queue, worker.Options{
		EnableSessionWorker:               true,
		MaxConcurrentSessionExecutionSize: 1,
	})
	for _, definition := range targetRepositoryActivities {
		definition := definition
		w.RegisterActivityWithOptions(
			func(_ context.Context, in sessionActivityInput) (sessionActivityEvidence, error) {
				in.Operation = definition.Operation
				return runSessionActivity(root, identity, in)
			},
			activity.RegisterOptions{Name: definition.Name},
		)
	}
	for _, definition := range replacementActivities {
		definition := definition
		w.RegisterActivityWithOptions(
			func(_ context.Context, in sessionActivityInput) (sessionActivityEvidence, error) {
				in.Operation = definition.Operation
				return runSessionActivity(root, identity, in)
			},
			activity.RegisterOptions{Name: definition.Name},
		)
	}
	target, err := productionRunWorkerActivities(root)
	if err != nil {
		return nil, fmt.Errorf("composing registered Run Worker activities: %w", err)
	}
	w.RegisterActivity(target)
	return w, nil
}

func runSessionActivity(root, identity string, in sessionActivityInput) (sessionActivityEvidence, error) {
	markerPath, err := privateMarkerPath(root, in.MarkerName)
	if err != nil {
		return sessionActivityEvidence{}, fmt.Errorf("resolve private marker path: %w", err)
	}
	if in.Write {
		if err := os.WriteFile(markerPath, []byte(in.Marker), 0o600); err != nil {
			return sessionActivityEvidence{}, fmt.Errorf("writing filesystem marker: %w", err)
		}
	}
	evidence := sessionActivityEvidence{Operation: in.Operation, Worker: identity, ProcessID: os.Getpid()}
	marker, err := os.ReadFile(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		if in.Operation == "reconcile_attempt" {
			evidence.Recovery = attemptRecoveryUnresumable
		}
		return evidence, nil
	}
	if err != nil {
		return sessionActivityEvidence{}, fmt.Errorf("reading filesystem marker: %w", err)
	}
	evidence.Found = true
	evidence.Marker = string(marker)
	return evidence, nil
}

func productionRunWorkerActivities(root string) (*activities.RunWorkerActivities, error) {
	recorder := productionRecorder{root: root, identity: productionRunWorkerIdentity}
	repository := &productionRepositoryCheckpoint{}
	return activities.NewRunWorkerActivities(activities.RunWorkerDeps{
		Clock:                 productionClock{},
		Repository:            productionRepository{recorder: recorder},
		GitHub:                productionGitHub{recorder: recorder},
		Identity:              productionRunWorkerIdentity,
		Branch:                work.FactoryTicketBranchName(42, productionRunWorkerIdentity.RunID),
		RepositoryCheckpoints: repository.open,
	})
}

type productionRecorder struct {
	root     string
	identity work.RunWorkerIdentity
}

func (r productionRecorder) observe(operation string) error {
	evidence := productionEvidence{Identity: r.identity, ProcessID: os.Getpid()}
	raw, err := os.ReadFile(filepath.Join(r.root, productionEvidenceFilename))
	if err == nil {
		if err := json.Unmarshal(raw, &evidence); err != nil {
			return fmt.Errorf("decoding production Run Worker evidence: %w", err)
		}
		if evidence.Identity != r.identity || evidence.ProcessID != os.Getpid() {
			return fmt.Errorf("production Run Worker identity or process changed: %w", work.ErrPermanent)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading production Run Worker evidence: %w", err)
	}
	evidence.Operations = append(evidence.Operations, operation)
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("encoding production Run Worker evidence: %w", err)
	}
	if err := os.WriteFile(filepath.Join(r.root, productionEvidenceFilename), encoded, 0o600); err != nil {
		return fmt.Errorf("writing production Run Worker evidence: %w", err)
	}
	return nil
}

func readProductionEvidence(t *testing.T, root string) productionEvidence {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, productionEvidenceFilename))
	if err != nil {
		t.Fatalf("reading production Run Worker evidence: %v", err)
	}
	var evidence productionEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatalf("decoding production Run Worker evidence: %v", err)
	}
	return evidence
}

type productionRepository struct{ recorder productionRecorder }

func (r productionRepository) Prepare(context.Context, string, string) (string, error) {
	if err := r.recorder.observe("clone"); err != nil {
		return "", fmt.Errorf("recording target repository clone: %w", err)
	}
	return "candidate-head", nil
}

func (r productionRepository) PrepareFromCommit(context.Context, string, string, string) (string, error) {
	if err := r.recorder.observe("clone_from_commit"); err != nil {
		return "", fmt.Errorf("recording target repository recovery clone: %w", err)
	}
	return "candidate-head", nil
}

func (r productionRepository) Publish(context.Context, string) (string, error) {
	if err := r.recorder.observe("publish"); err != nil {
		return "", fmt.Errorf("recording target repository publication: %w", err)
	}
	return "candidate-head", nil
}

type productionClock struct{}

func (productionClock) Now() time.Time { return time.Date(2026, 7, 31, 20, 0, 0, 0, time.UTC) }

type productionRepositoryCheckpoint struct {
	value store.GitCheckpoint
	found bool
}

func (c *productionRepositoryCheckpoint) open(identity work.RunWorkerIdentity) (activities.RepositoryCheckpoint, error) {
	if identity != productionRunWorkerIdentity {
		return nil, fmt.Errorf("opening repository checkpoint for unexpected identity %#v: %w", identity, work.ErrPermanent)
	}
	return c, nil
}

func (c *productionRepositoryCheckpoint) Load(context.Context) (store.GitCheckpoint, bool, error) {
	return c.value, c.found, nil
}

func (c *productionRepositoryCheckpoint) Checkpoint(_ context.Context, value store.GitCheckpointInput) (store.GitCheckpoint, error) {
	c.value, c.found = value.GitCheckpoint, true
	return c.value, nil
}

func (c *productionRepositoryCheckpoint) CheckpointEffect(_ context.Context, value store.GitCheckpointInput) (store.GitCheckpoint, error) {
	c.value, c.found = value.GitCheckpoint, true
	return c.value, nil
}

type productionGitHub struct{ recorder productionRecorder }

func (g productionGitHub) PullRequestForBranch(context.Context, string) (work.PullRequest, bool, error) {
	return work.PullRequest{}, false, nil
}

func (g productionGitHub) OpenOrUpdatePullRequest(context.Context, string, string, string, *work.PullRequest) (work.PullRequest, error) {
	if err := g.recorder.observe("sync"); err != nil {
		return work.PullRequest{}, fmt.Errorf("recording target pull request sync: %w", err)
	}
	return work.PullRequest{Number: 42, NodeID: "PR_kwDO", HeadSHA: "candidate-head", Draft: true}, nil
}

func (g productionGitHub) MarkPullRequestReadyForReview(context.Context, string) error {
	if err := g.recorder.observe("ready"); err != nil {
		return fmt.Errorf("recording target pull request ready: %w", err)
	}
	return nil
}

func (g productionGitHub) MergePullRequest(context.Context, int, string) (work.PullRequestMergeResult, error) {
	if err := g.recorder.observe("merge"); err != nil {
		return work.PullRequestMergeResult{}, fmt.Errorf("recording target pull request merge: %w", err)
	}
	return work.PullRequestMergeResult{Outcome: work.PullRequestMergeConfirmed, MergeSHA: "merge-head"}, nil
}

func (g productionGitHub) ChecksForCommit(context.Context, string, []string) ([]work.CheckRun, error) {
	if err := g.recorder.observe("ci"); err != nil {
		return nil, fmt.Errorf("recording target CI observation: %w", err)
	}
	return []work.CheckRun{{Name: "test", Completed: true, Conclusion: "success"}}, nil
}

func (g productionGitHub) RetirePullRequest(context.Context, int) (work.PullRequestRetirement, error) {
	if err := g.recorder.observe("retire"); err != nil {
		return work.PullRequestRetirement{}, fmt.Errorf("recording target pull request retirement: %w", err)
	}
	return work.PullRequestRetirement{}, nil
}

func privateMarkerPath(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || filepath.Base(name) != name {
		return "", fmt.Errorf("marker name %q must be one relative path element", name)
	}
	return filepath.Join(root, name), nil
}

func startPrivateWorkerProcess(t *testing.T, hostPort, queue, identity, root string) *privateWorkerProcess {
	t.Helper()
	process := &privateWorkerProcess{}
	process.cmd = exec.Command(
		os.Args[0],
		"-test.run=^TestPrivateWorkerHelperProcess$",
		"-test.count=1",
		"-capability-private-worker-helper=true",
		"-capability-temporal-host-port="+hostPort,
		"-capability-private-queue="+queue,
		"-capability-private-identity="+identity,
		"-capability-private-root="+root,
	)
	process.cmd.Stdout = &process.output
	process.cmd.Stderr = &process.output
	if err := process.cmd.Start(); err != nil {
		t.Fatalf("starting %s helper process: %v", identity, err)
	}
	t.Cleanup(func() { process.stop(t) })
	waitForFile(t, filepath.Join(root, ".worker-ready"), identity+" ready marker")
	return process
}

func (p *privateWorkerProcess) processID() int {
	return p.cmd.Process.Pid
}

func (p *privateWorkerProcess) stop(t *testing.T) {
	t.Helper()
	if p.stopped {
		return
	}
	p.stopped = true
	if err := p.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Errorf("interrupting private worker process %d: %v", p.processID(), err)
		return
	}
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("private worker process %d stopped with %v; output: %s", p.processID(), err, p.output.String())
		}
	case <-time.After(10 * time.Second):
		if err := p.cmd.Process.Kill(); err != nil {
			t.Errorf("killing stuck private worker process %d: %v", p.processID(), err)
		}
		<-done
		t.Errorf("private worker process %d did not stop in time; output: %s", p.processID(), p.output.String())
	}
}

func waitForFile(t *testing.T, path, description string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("checking %s: %v", description, err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", description)
		case <-tick.C:
		}
	}
}

func startServer(t *testing.T) *testsuite.DevServer {
	t.Helper()
	cacheDir := filepath.Join(os.TempDir(), "software-factory-temporal-cli")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatalf("creating Temporal CLI cache: %v", err)
	}
	server, err := testsuite.StartDevServer(context.Background(), testsuite.DevServerOptions{
		CachedDownload: testsuite.CachedDownload{
			Version: strings.TrimSpace(temporalCLIVersion),
			DestDir: cacheDir,
		},
		LogLevel: "error",
	})
	if err != nil {
		t.Fatalf("starting Temporal dev server: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Stop(); err != nil {
			t.Errorf("stopping Temporal dev server: %v", err)
		}
	})
	return server
}

func startWorker(t *testing.T, w worker.Worker) {
	t.Helper()
	if err := w.Start(); err != nil {
		t.Fatalf("starting worker: %v", err)
	}
	t.Cleanup(w.Stop)
}
