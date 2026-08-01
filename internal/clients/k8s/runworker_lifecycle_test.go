package k8s

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

func validRunWorkerSecrets() work.RunWorkerSecretMaterial {
	return work.RunWorkerSecretMaterial{
		GitHubToken:          work.NewCredential("ghs_initial_secret"),
		GitHubLogin:          "www-software-factory-bot[bot]",
		GitHubExpiresAt:      time.Date(2026, 7, 31, 19, 0, 0, 0, time.UTC),
		CheckpointCapability: work.NewCredential("checkpoint-secret"),
	}
}

func mustRunWorkers(t *testing.T, cs *fake.Clientset) *RunWorkers {
	t.Helper()
	workers, err := newRunWorkers(cs, "software-factory", discardLogger(), "")
	if err != nil {
		t.Fatalf("newRunWorkers: %v", err)
	}
	return workers
}

func TestProvisionRejectsDriftBeforeMutatingGenerationSecrets(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset()
	workers := mustRunWorkers(t, cs)
	spec := validRunWorkerSpec()
	id, err := workers.Provision(ctx, spec, validRunWorkerSecrets())
	if err != nil {
		t.Fatalf("initial Provision: %v", err)
	}
	pod, err := cs.CoreV1().Pods("software-factory").Get(ctx, string(id), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.Spec.AutomountServiceAccountToken = ptr(true)
	if _, err := cs.CoreV1().Pods("software-factory").Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	changed := validRunWorkerSecrets()
	changed.GitHubToken = work.NewCredential("must-not-be-installed")
	if _, err := workers.Provision(ctx, spec, changed); err == nil {
		t.Fatal("Provision accepted a drifted existing generation")
	}
	secret, err := cs.CoreV1().Secrets("software-factory").Get(ctx, runWorkerGitHubSecretName(id), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(secret.Data[runWorkerGitHubTokenKey]) != "ghs_initial_secret" || string(secret.Data[runWorkerGitHubRevisionKey]) != "1" {
		t.Fatalf("drift rejection mutated credential Secret: %+v", secret.Data)
	}
}

func TestProvisionExactRetryLeavesPodAndSecretsStable(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset()
	workers := mustRunWorkers(t, cs)
	spec := validRunWorkerSpec()
	material := validRunWorkerSecrets()
	id, err := workers.Provision(ctx, spec, material)
	if err != nil {
		t.Fatalf("initial Provision: %v", err)
	}
	before, err := cs.CoreV1().Secrets("software-factory").Get(ctx, runWorkerGitHubSecretName(id), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	actionCount := len(cs.Actions())
	retryID, err := workers.Provision(ctx, spec, material)
	if err != nil {
		t.Fatalf("retry Provision: %v", err)
	}
	if retryID != id {
		t.Fatalf("retry ID = %q, want %q", retryID, id)
	}
	after, err := cs.CoreV1().Secrets("software-factory").Get(ctx, runWorkerGitHubSecretName(id), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if before.ResourceVersion != after.ResourceVersion || string(after.Data[runWorkerGitHubRevisionKey]) != "1" {
		t.Fatalf("exact retry mutated GitHub Secret: before rv=%q after rv=%q revision=%q", before.ResourceVersion, after.ResourceVersion, after.Data[runWorkerGitHubRevisionKey])
	}
	for _, action := range cs.Actions()[actionCount:] {
		if action.GetVerb() == "update" || action.GetVerb() == "patch" || action.GetVerb() == "delete" {
			t.Fatalf("exact retry performed mutating Kubernetes action: %s %s", action.GetVerb(), action.GetResource().Resource)
		}
	}
}

func TestListRunWorkersDiscoversUniquePodAndSecretIdentities(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset()
	workers := mustRunWorkers(t, cs)
	first := validRunWorkerSpec()
	if _, err := workers.Provision(ctx, first, validRunWorkerSecrets()); err != nil {
		t.Fatalf("Provision(first): %v", err)
	}
	second := first.Identity
	second.Generation = 2
	if _, err := cs.CoreV1().Secrets("software-factory").Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "surviving-run-worker-secret",
		Labels: map[string]string{
			labelName:       "software-factory-run-worker",
			labelManagedBy:  labelManagedByValue,
			labelRunID:      second.RunID,
			labelGeneration: "2",
		},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating orphaned Run Worker Secret: %v", err)
	}

	got, err := workers.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []work.RunWorkerIdentity{first.Identity, second}
	if len(got) != len(want) {
		t.Fatalf("List() = %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("List()[%d] = %+v, want %+v", index, got[index], want[index])
		}
	}
}

func TestNewRunWorkersValidatesOnlyItsTargetDependencies(t *testing.T) {
	t.Parallel()

	if _, err := newRunWorkers(fake.NewSimpleClientset(), "Not/A/Namespace", discardLogger(), ""); err == nil {
		t.Fatal("newRunWorkers accepted an invalid namespace")
	}
	if _, err := newRunWorkers(fake.NewSimpleClientset(), "software-factory", nil, ""); err == nil {
		t.Fatal("newRunWorkers accepted a nil logger")
	}
	if _, err := newRunWorkers(fake.NewSimpleClientset(), "software-factory", discardLogger(), "Not/A/Secret"); err == nil {
		t.Fatal("newRunWorkers accepted an invalid image pull Secret")
	}
	if _, err := newRunWorkers(nil, "software-factory", discardLogger(), ""); err == nil {
		t.Fatal("newRunWorkers accepted a nil clientset")
	}
	if _, err := newRunWorkers(fake.NewSimpleClientset(), "software-factory", discardLogger(), ""); err != nil {
		t.Fatalf("newRunWorkers rejected its minimal valid dependencies: %v", err)
	}
}

func TestProvisionRunWorkerCreatesPodAndGenerationSecrets(t *testing.T) {
	cs := fake.NewSimpleClientset()
	workers := mustRunWorkers(t, cs)
	id, err := workers.Provision(context.Background(), validRunWorkerSpec(), validRunWorkerSecrets())
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	wantID, err := work.RunWorkerName(validRunWorkerSpec().Identity)
	if err != nil {
		t.Fatal(err)
	}
	if id != wantID {
		t.Errorf("ID = %q", id)
	}
	if _, err := cs.CoreV1().Pods("software-factory").Get(context.Background(), string(id), metav1.GetOptions{}); err != nil {
		t.Errorf("pod: %v", err)
	}
	for _, name := range []string{runWorkerGitHubSecretName(id), runWorkerCheckpointSecretName(id)} {
		if _, err := cs.CoreV1().Secrets("software-factory").Get(context.Background(), name, metav1.GetOptions{}); err != nil {
			t.Errorf("secret %s: %v", name, err)
		}
	}
}

func TestInstallRepositoryCapabilityReusesTheGenerationValue(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset()
	workers := mustRunWorkers(t, cs)
	identity := validRunWorkerSpec().Identity
	if _, err := workers.Provision(ctx, validRunWorkerSpec(), validRunWorkerSecrets()); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	first, err := workers.InstallRepositoryCapability(ctx, identity, work.NewCredential("repository-first"))
	if err != nil {
		t.Fatalf("InstallRepositoryCapability(first): %v", err)
	}
	retry, err := workers.InstallRepositoryCapability(ctx, identity, work.NewCredential("repository-retry"))
	if err != nil {
		t.Fatalf("InstallRepositoryCapability(retry): %v", err)
	}
	if first.Reveal() != "repository-first" || retry.Reveal() != "repository-first" {
		t.Fatalf("installed/retry = %q/%q", first.Reveal(), retry.Reveal())
	}
	id, _ := work.RunWorkerName(identity)
	secret, err := cs.CoreV1().Secrets("software-factory").Get(ctx, runWorkerRepositorySecretName(id), metav1.GetOptions{})
	if err != nil || string(secret.Data[runWorkerRepositoryKey]) != "repository-first" {
		t.Fatalf("repository Secret = %+v, %v", secret, err)
	}
}

func TestRotateGitHubCredentialReturnsOnlyRevisionAndExpiry(t *testing.T) {
	cs := fake.NewSimpleClientset()
	workers := mustRunWorkers(t, cs)
	id, err := workers.Provision(context.Background(), validRunWorkerSpec(), validRunWorkerSecrets())
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	expires := time.Date(2026, 7, 31, 19, 30, 0, 0, time.UTC)
	got, err := workers.RotateGitHubCredential(context.Background(), validRunWorkerSpec().Identity, work.NewCredential("ghs_rotated_secret"), "bot[bot]", expires)
	if err != nil {
		t.Fatalf("RotateGitHubCredential: %v", err)
	}
	if got.Revision != "2" || !got.ExpiresAt.Equal(expires) {
		t.Errorf("safe result = %+v", got)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if strings.Contains(string(raw), "ghs_") {
		t.Errorf("rotation result leaked token: %s", raw)
	}
	secret, err := cs.CoreV1().Secrets("software-factory").Get(context.Background(), runWorkerGitHubSecretName(id), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read rotated Secret: %v", err)
	}
	if string(secret.Data[runWorkerGitHubTokenKey]) != "ghs_rotated_secret" || string(secret.Data[runWorkerGitHubRevisionKey]) != "2" {
		t.Error("rotated Secret data did not advance")
	}
}

func TestInstallCheckpointCapabilityReusesTheExactAttemptsProjectedValue(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset()
	workers := mustRunWorkers(t, cs)
	spec := validRunWorkerSpec()
	if _, err := workers.Provision(ctx, spec, validRunWorkerSecrets()); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	attempt := store.TargetAttemptID{RunID: spec.Identity.RunID, StepOrdinal: 3, AttemptNo: 1}
	first, err := workers.InstallCheckpointCapability(ctx, spec.Identity, attempt, work.NewCredential("first-attempt-secret"))
	if err != nil {
		t.Fatalf("InstallCheckpointCapability: %v", err)
	}
	retry, err := workers.InstallCheckpointCapability(ctx, spec.Identity, attempt, work.NewCredential("must-not-replace"))
	if err != nil {
		t.Fatalf("InstallCheckpointCapability retry: %v", err)
	}
	if first.Reveal() != "first-attempt-secret" || retry.Reveal() != "first-attempt-secret" {
		t.Fatal("exact retry replaced the projected capability")
	}
}

func TestInstallCheckpointCapabilityRejectsAnotherRunBeforeCallingKubernetes(t *testing.T) {
	cs := fake.NewSimpleClientset()
	workers := mustRunWorkers(t, cs)
	identity := validRunWorkerSpec().Identity
	before := len(cs.Actions())
	_, err := workers.InstallCheckpointCapability(context.Background(), identity, store.TargetAttemptID{
		RunID: "5cc6ca8d-7af5-42f3-965f-4d9764fcaf53", StepOrdinal: 1, AttemptNo: 1,
	}, work.NewCredential("secret"))
	if err == nil {
		t.Fatal("InstallCheckpointCapability accepted another Run")
	}
	if got := len(cs.Actions()); got != before {
		t.Fatalf("invalid Attempt called Kubernetes: %d actions", got-before)
	}
}

func TestDeleteRunWorkerRemovesPodAndAllGenerationSecrets(t *testing.T) {
	cs := fake.NewSimpleClientset()
	workers := mustRunWorkers(t, cs)
	id, err := workers.Provision(context.Background(), validRunWorkerSpec(), validRunWorkerSecrets())
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := workers.Delete(context.Background(), validRunWorkerSpec().Identity); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := cs.CoreV1().Pods("software-factory").Get(context.Background(), string(id), metav1.GetOptions{}); err == nil {
		t.Error("pod still exists")
	}
	for _, name := range runWorkerSecretNames(id) {
		if _, err := cs.CoreV1().Secrets("software-factory").Get(context.Background(), name, metav1.GetOptions{}); err == nil {
			t.Errorf("secret %s still exists", name)
		}
	}
}

func TestDeleteRejectsInvalidIdentityBeforeCallingKubernetes(t *testing.T) {
	cs := fake.NewSimpleClientset()
	workers := mustRunWorkers(t, cs)
	if err := workers.Delete(context.Background(), work.RunWorkerIdentity{}); err == nil {
		t.Fatal("Delete accepted an invalid identity")
	}
	if actions := cs.Actions(); len(actions) != 0 {
		t.Fatalf("Delete called Kubernetes for invalid identity: %+v", actions)
	}
}
