package k8s

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

const (
	runWorkerGitHubSecretPrefix     = "run-worker-github-"
	runWorkerCheckpointSecretPrefix = "run-worker-checkpoint-"
	runWorkerRepositorySecretPrefix = "run-worker-repository-"
	runWorkerGitHubTokenKey         = "token"
	runWorkerGitHubLoginKey         = "login"
	runWorkerGitHubRevisionKey      = "revision"
	runWorkerGitHubExpiresAtKey     = "expires-at"
	runWorkerCheckpointKey          = "capability"
	runWorkerCheckpointAttempt      = "software-factory.worldwidewebb.co/checkpoint-attempt"
	runWorkerRepositoryKey          = "capability"
)

func runWorkerGitHubSecretName(id work.RunWorkerID) string {
	return runWorkerGitHubSecretPrefix + string(id)
}

func runWorkerCheckpointSecretName(id work.RunWorkerID) string {
	return runWorkerCheckpointSecretPrefix + string(id)
}

func runWorkerRepositorySecretName(id work.RunWorkerID) string {
	return runWorkerRepositorySecretPrefix + string(id)
}

func runWorkerSecretNames(id work.RunWorkerID) []string {
	return []string{runWorkerGitHubSecretName(id), runWorkerCheckpointSecretName(id), runWorkerRepositorySecretName(id)}
}

// Provision creates one target worker generation and all of its file-only
// capabilities. It returns before Temporal's Session handshake proves ready.
func (r *RunWorkers) Provision(ctx context.Context, spec work.RunWorkerSpec, material work.RunWorkerSecretMaterial) (work.RunWorkerID, error) {
	pod, err := buildRunWorkerPod(spec, r.opts)
	if err != nil {
		return "", fmt.Errorf("provisioning Run Worker: %w", err)
	}
	if err := validateRunWorkerSecretMaterial(material); err != nil {
		return "", fmt.Errorf("provisioning Run Worker: %w", err)
	}
	id, err := work.ParseRunWorkerID(pod.Name, spec.Identity)
	if err != nil {
		return "", fmt.Errorf("provisioning Run Worker: %w", err)
	}
	labels := runWorkerLabels(spec)
	pods := r.cs.CoreV1().Pods(r.ns)
	if _, err := pods.Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return "", fmt.Errorf("provisioning Run Worker %s: creating pod: %w", id, err)
		}
		existing, getErr := pods.Get(ctx, pod.Name, metav1.GetOptions{})
		if getErr != nil {
			return "", fmt.Errorf("provisioning Run Worker %s: reading existing pod: %w", id, getErr)
		}
		if !runWorkerPodMatches(existing, pod) {
			return "", fmt.Errorf("provisioning Run Worker %s: existing generation differs from its authoritative spec: %w", id, work.ErrPermanent)
		}
	}

	// The Pod is created or its complete target contract was proved compatible
	// before any credential is updated. A retry can therefore never rotate a
	// live but different generation's Secrets and only then discover the drift.
	if err := r.putSecret(ctx, runWorkerCheckpointSecretName(id), labels, map[string][]byte{
		runWorkerCheckpointKey: []byte(material.CheckpointCapability.Reveal()),
	}); err != nil {
		return "", fmt.Errorf("provisioning Run Worker %s checkpoint capability: %w", id, err)
	}
	if _, err := r.putGitHubSecret(ctx, id, labels, material.GitHubToken, material.GitHubLogin, material.GitHubExpiresAt); err != nil {
		return "", fmt.Errorf("provisioning Run Worker %s GitHub credential: %w", id, err)
	}
	r.logger.InfoContext(ctx, "Run Worker provisioned", "run_worker", id, "run_id", spec.Identity.RunID, "generation", spec.Identity.Generation, "image", spec.Image)
	return id, nil
}

// RotateGitHubCredential atomically replaces the projected GitHub files and
// returns only the revision the Run Worker can observe plus its expiry.
func (r *RunWorkers) RotateGitHubCredential(ctx context.Context, identity work.RunWorkerIdentity, token work.Credential, login string, expiresAt time.Time) (work.RunWorkerCredentialRevision, error) {
	id, err := work.RunWorkerName(identity)
	if err != nil {
		return work.RunWorkerCredentialRevision{}, fmt.Errorf("rotating Run Worker GitHub credential: %w", err)
	}
	if strings.TrimSpace(token.Reveal()) == "" || strings.TrimSpace(login) == "" || expiresAt.IsZero() {
		return work.RunWorkerCredentialRevision{}, fmt.Errorf("rotating Run Worker GitHub credential requires token, login, and expiry: %w", work.ErrPermanent)
	}
	secret, err := r.cs.CoreV1().Secrets(r.ns).Get(ctx, runWorkerGitHubSecretName(id), metav1.GetOptions{})
	if err != nil {
		return work.RunWorkerCredentialRevision{}, fmt.Errorf("reading Run Worker %s GitHub Secret: %w", id, err)
	}
	result, err := r.updateGitHubSecret(ctx, secret, token, login, expiresAt)
	if err != nil {
		return work.RunWorkerCredentialRevision{}, fmt.Errorf("rotating Run Worker %s GitHub credential: %w", id, err)
	}
	r.logger.InfoContext(ctx, "Run Worker GitHub credential rotated", "run_worker", id, "revision", result.Revision, "expires_at", result.ExpiresAt)
	return result, nil
}

// InstallCheckpointCapability atomically projects one exact Attempt's narrow
// API capability. A retry for that Attempt returns the already installed
// value so a lost activity response cannot bind a different capability.
func (r *RunWorkers) InstallCheckpointCapability(ctx context.Context, identity work.RunWorkerIdentity, attemptID store.TargetAttemptID, proposed work.Credential) (work.Credential, error) {
	id, err := work.RunWorkerName(identity)
	if err != nil {
		return work.Credential{}, fmt.Errorf("installing Run Worker checkpoint capability: %w", err)
	}
	if attemptID.RunID != identity.RunID || attemptID.StepOrdinal <= 0 || attemptID.AttemptNo <= 0 || strings.TrimSpace(proposed.Reveal()) == "" {
		return work.Credential{}, fmt.Errorf("installing Run Worker checkpoint capability requires this Run's exact Attempt and a value: %w", work.ErrPermanent)
	}
	secrets := r.cs.CoreV1().Secrets(r.ns)
	secret, err := secrets.Get(ctx, runWorkerCheckpointSecretName(id), metav1.GetOptions{})
	if err != nil {
		return work.Credential{}, fmt.Errorf("reading Run Worker %s checkpoint Secret: %w", id, err)
	}
	wantAttempt := attemptID.String()
	if secret.Annotations[runWorkerCheckpointAttempt] == wantAttempt {
		installed := strings.TrimSpace(string(secret.Data[runWorkerCheckpointKey]))
		if installed == "" {
			return work.Credential{}, fmt.Errorf("run worker %s checkpoint Secret has no installed capability: %w", id, work.ErrPermanent)
		}
		return work.NewCredential(installed), nil
	}
	secret.Data = map[string][]byte{runWorkerCheckpointKey: []byte(proposed.Reveal())}
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	secret.Annotations[runWorkerCheckpointAttempt] = wantAttempt
	if _, err := secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		return work.Credential{}, fmt.Errorf("updating Run Worker %s checkpoint Secret: %w", id, err)
	}
	return proposed, nil
}

// InstallRepositoryCapability creates one generation's repository capability
// Secret exactly once. A retry returns the already projected value so a lost
// activity response cannot rotate the Store to a different value.
func (r *RunWorkers) InstallRepositoryCapability(ctx context.Context, identity work.RunWorkerIdentity, proposed work.Credential) (work.Credential, error) {
	id, err := work.RunWorkerName(identity)
	if err != nil {
		return work.Credential{}, fmt.Errorf("installing Run Worker repository capability: %w", err)
	}
	if strings.TrimSpace(proposed.Reveal()) == "" {
		return work.Credential{}, fmt.Errorf("installing Run Worker repository capability requires a value: %w", work.ErrPermanent)
	}
	pod, err := r.cs.CoreV1().Pods(r.ns).Get(ctx, string(id), metav1.GetOptions{})
	if err != nil {
		return work.Credential{}, fmt.Errorf("reading Run Worker %s before repository capability install: %w", id, err)
	}
	if pod.Labels[labelRunID] != identity.RunID || pod.Labels[labelGeneration] != strconv.Itoa(identity.Generation) {
		return work.Credential{}, fmt.Errorf("run worker %s does not match repository capability identity: %w", id, work.ErrPermanent)
	}
	secrets := r.cs.CoreV1().Secrets(r.ns)
	name := runWorkerRepositorySecretName(id)
	want := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: pod.Labels}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{runWorkerRepositoryKey: []byte(proposed.Reveal())}}
	if _, err := secrets.Create(ctx, want, metav1.CreateOptions{}); err == nil {
		return proposed, nil
	} else if !apierrors.IsAlreadyExists(err) {
		return work.Credential{}, fmt.Errorf("creating Run Worker %s repository capability Secret: %w", id, err)
	}
	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return work.Credential{}, fmt.Errorf("reading Run Worker %s repository capability Secret: %w", id, err)
	}
	installed := strings.TrimSpace(string(existing.Data[runWorkerRepositoryKey]))
	if existing.Type != corev1.SecretTypeOpaque || !reflect.DeepEqual(existing.Labels, pod.Labels) || len(existing.Data) != 1 || installed == "" {
		return work.Credential{}, fmt.Errorf("run worker %s repository capability Secret differs from its generation: %w", id, work.ErrPermanent)
	}
	return work.NewCredential(installed), nil
}

// List returns the unique Run Worker identities discoverable from Pods and
// projected Secrets. It reads metadata only, so maintenance can recover a
// terminal Run whose pod or one of its Secrets survived a failed teardown
// without exposing any credential value.
func (r *RunWorkers) List(ctx context.Context) ([]work.RunWorkerIdentity, error) {
	selector := labels.SelectorFromSet(labels.Set{labelName: "software-factory-run-worker", labelManagedBy: labelManagedByValue}).String()
	pods, err := r.cs.CoreV1().Pods(r.ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("listing Run Worker Pods: %w", err)
	}
	secrets, err := r.cs.CoreV1().Secrets(r.ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("listing Run Worker Secrets: %w", err)
	}
	identities := make(map[work.RunWorkerIdentity]bool, len(pods.Items)+len(secrets.Items))
	for _, pod := range pods.Items {
		identity, err := runWorkerIdentityFromLabels(pod.Labels)
		if err != nil {
			return nil, fmt.Errorf("reading Run Worker Pod %s identity: %w", pod.Name, err)
		}
		identities[identity] = true
	}
	for _, secret := range secrets.Items {
		identity, err := runWorkerIdentityFromLabels(secret.Labels)
		if err != nil {
			return nil, fmt.Errorf("reading Run Worker Secret %s identity: %w", secret.Name, err)
		}
		identities[identity] = true
	}
	result := make([]work.RunWorkerIdentity, 0, len(identities))
	for identity := range identities {
		result = append(result, identity)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].RunID == result[right].RunID {
			return result[left].Generation < result[right].Generation
		}
		return result[left].RunID < result[right].RunID
	})
	return result, nil
}

func runWorkerIdentityFromLabels(labels map[string]string) (work.RunWorkerIdentity, error) {
	generation, err := strconv.Atoi(labels[labelGeneration])
	if err != nil {
		return work.RunWorkerIdentity{}, fmt.Errorf("parsing generation %q: %w", labels[labelGeneration], err)
	}
	identity, err := work.NewRunWorkerIdentity(labels[labelRunID], generation)
	if err != nil {
		return work.RunWorkerIdentity{}, err
	}
	return identity, nil
}

// Delete removes a target worker and every per-generation Secret. Absence is
// success so Temporal cleanup retries are idempotent.
func (r *RunWorkers) Delete(ctx context.Context, identity work.RunWorkerIdentity) error {
	id, err := work.RunWorkerName(identity)
	if err != nil {
		return fmt.Errorf("deleting Run Worker: %w", err)
	}
	if err := ignoreAbsent(r.cs.CoreV1().Pods(r.ns).Delete(ctx, string(id), metav1.DeleteOptions{})); err != nil {
		return fmt.Errorf("deleting Run Worker %s: %w", id, err)
	}
	for _, name := range runWorkerSecretNames(id) {
		if err := ignoreAbsent(r.cs.CoreV1().Secrets(r.ns).Delete(ctx, name, metav1.DeleteOptions{})); err != nil {
			return fmt.Errorf("deleting Run Worker %s Secret %s: %w", id, name, err)
		}
	}
	r.logger.InfoContext(ctx, "Run Worker deleted", "run_worker", id)
	return nil
}

func validateRunWorkerSecretMaterial(material work.RunWorkerSecretMaterial) error {
	if strings.TrimSpace(material.GitHubToken.Reveal()) == "" ||
		strings.TrimSpace(material.GitHubLogin) == "" || material.GitHubExpiresAt.IsZero() ||
		strings.TrimSpace(material.CheckpointCapability.Reveal()) == "" {
		return fmt.Errorf("run worker provisioning requires GitHub and checkpoint file material: %w", work.ErrPermanent)
	}
	return nil
}

func (r *RunWorkers) putSecret(ctx context.Context, name string, labels map[string]string, data map[string][]byte) error {
	secrets := r.cs.CoreV1().Secrets(r.ns)
	want := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Type: corev1.SecretTypeOpaque, Data: data}
	if _, err := secrets.Create(ctx, want, metav1.CreateOptions{}); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating Run Worker Secret %s: %w", name, err)
	}
	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("reading Run Worker Secret %s: %w", name, err)
	}
	if reflect.DeepEqual(existing.Labels, labels) && reflect.DeepEqual(existing.Data, data) && existing.Type == corev1.SecretTypeOpaque {
		return nil
	}
	existing.Labels = labels
	existing.Data = data
	if _, err := secrets.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating Run Worker Secret %s: %w", name, err)
	}
	return nil
}

func (r *RunWorkers) putGitHubSecret(ctx context.Context, id work.RunWorkerID, labels map[string]string, token work.Credential, login string, expiresAt time.Time) (work.RunWorkerCredentialRevision, error) {
	secrets := r.cs.CoreV1().Secrets(r.ns)
	want := githubSecret(runWorkerGitHubSecretName(id), labels, token, login, expiresAt, 1)
	if _, err := secrets.Create(ctx, want, metav1.CreateOptions{}); err == nil {
		return work.RunWorkerCredentialRevision{Revision: "1", ExpiresAt: expiresAt.UTC()}, nil
	} else if !apierrors.IsAlreadyExists(err) {
		return work.RunWorkerCredentialRevision{}, fmt.Errorf("creating Run Worker GitHub Secret: %w", err)
	}
	existing, err := secrets.Get(ctx, want.Name, metav1.GetOptions{})
	if err != nil {
		return work.RunWorkerCredentialRevision{}, fmt.Errorf("reading Run Worker GitHub Secret: %w", err)
	}
	current, revisionErr := runWorkerGitHubRevision(existing)
	if revisionErr != nil {
		return work.RunWorkerCredentialRevision{}, revisionErr
	}
	if existing.Type == corev1.SecretTypeOpaque && reflect.DeepEqual(existing.Labels, labels) && bytes.Equal(existing.Data[runWorkerGitHubTokenKey], []byte(token.Reveal())) &&
		bytes.Equal(existing.Data[runWorkerGitHubLoginKey], []byte(login)) &&
		bytes.Equal(existing.Data[runWorkerGitHubExpiresAtKey], []byte(expiresAt.UTC().Format(time.RFC3339Nano))) {
		return work.RunWorkerCredentialRevision{Revision: strconv.Itoa(current), ExpiresAt: expiresAt.UTC()}, nil
	}
	existing.Labels = labels
	return r.updateGitHubSecret(ctx, existing, token, login, expiresAt)
}

func (r *RunWorkers) updateGitHubSecret(ctx context.Context, secret *corev1.Secret, token work.Credential, login string, expiresAt time.Time) (work.RunWorkerCredentialRevision, error) {
	current, err := runWorkerGitHubRevision(secret)
	if err != nil {
		return work.RunWorkerCredentialRevision{}, fmt.Errorf("updating Run Worker GitHub Secret %s revision: %w", secret.Name, err)
	}
	revision := current + 1
	secret.Data = githubSecretData(token, login, expiresAt, revision)
	if _, err := r.cs.CoreV1().Secrets(r.ns).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		return work.RunWorkerCredentialRevision{}, fmt.Errorf("updating Run Worker GitHub Secret %s: %w", secret.Name, err)
	}
	return work.RunWorkerCredentialRevision{Revision: strconv.Itoa(revision), ExpiresAt: expiresAt.UTC()}, nil
}

func runWorkerGitHubRevision(secret *corev1.Secret) (int, error) {
	current, err := strconv.Atoi(string(secret.Data[runWorkerGitHubRevisionKey]))
	if err != nil || current < 1 {
		return 0, fmt.Errorf("run worker GitHub Secret %s has invalid revision metadata: %w", secret.Name, work.ErrPermanent)
	}
	return current, nil
}

func githubSecret(name string, labels map[string]string, token work.Credential, login string, expiresAt time.Time, revision int) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Type: corev1.SecretTypeOpaque, Data: githubSecretData(token, login, expiresAt, revision)}
}

func githubSecretData(token work.Credential, login string, expiresAt time.Time, revision int) map[string][]byte {
	return map[string][]byte{
		runWorkerGitHubTokenKey:     []byte(token.Reveal()),
		runWorkerGitHubLoginKey:     []byte(login),
		runWorkerGitHubRevisionKey:  []byte(strconv.Itoa(revision)),
		runWorkerGitHubExpiresAtKey: []byte(expiresAt.UTC().Format(time.RFC3339Nano)),
	}
}

func runWorkerPodMatches(got, want *corev1.Pod) bool {
	gotFingerprint, gotErr := runWorkerPodFingerprint(got)
	wantFingerprint, wantErr := runWorkerPodFingerprint(want)
	return gotErr == nil && wantErr == nil && gotFingerprint == wantFingerprint
}

type runWorkerPodContract struct {
	Name                         string
	Labels                       map[string]string
	Annotations                  map[string]string
	RestartPolicy                corev1.RestartPolicy
	ActiveDeadlineSeconds        *int64
	AutomountServiceAccountToken *bool
	EnableServiceLinks           *bool
	TerminationGracePeriod       *int64
	ServiceAccountName           string
	SecurityContext              *corev1.PodSecurityContext
	ImagePullSecrets             []corev1.LocalObjectReference
	Containers                   []runWorkerContainerContract
	Volumes                      []corev1.Volume
}

type runWorkerContainerContract struct {
	Name            string
	Image           string
	ImagePullPolicy corev1.PullPolicy
	Command         []string
	Args            []string
	Env             []corev1.EnvVar
	Ports           []corev1.ContainerPort
	Resources       corev1.ResourceRequirements
	VolumeMounts    []corev1.VolumeMount
	SecurityContext *corev1.SecurityContext
}

func runWorkerPodFingerprint(pod *corev1.Pod) ([sha256.Size]byte, error) {
	if pod == nil || len(pod.Spec.Containers) != 2 {
		return [sha256.Size]byte{}, fmt.Errorf("fingerprinting Run Worker pod: expected repository and tool containers: %w", work.ErrPermanent)
	}
	serviceAccountName := pod.Spec.ServiceAccountName
	if serviceAccountName == "" {
		serviceAccountName = "default"
	}
	contract := runWorkerPodContract{
		Name: pod.Name, Labels: pod.Labels, Annotations: pod.Annotations, RestartPolicy: pod.Spec.RestartPolicy,
		ActiveDeadlineSeconds: pod.Spec.ActiveDeadlineSeconds, AutomountServiceAccountToken: pod.Spec.AutomountServiceAccountToken,
		EnableServiceLinks: pod.Spec.EnableServiceLinks, TerminationGracePeriod: pod.Spec.TerminationGracePeriodSeconds,
		ServiceAccountName: serviceAccountName, SecurityContext: pod.Spec.SecurityContext,
		ImagePullSecrets: pod.Spec.ImagePullSecrets, Volumes: pod.Spec.Volumes,
	}
	for _, container := range pod.Spec.Containers {
		contract.Containers = append(contract.Containers, runWorkerContainerContract{
			Name: container.Name, Image: container.Image, ImagePullPolicy: container.ImagePullPolicy,
			Command: container.Command, Args: container.Args, Env: container.Env, Ports: container.Ports, Resources: container.Resources,
			VolumeMounts: container.VolumeMounts, SecurityContext: container.SecurityContext,
		})
	}
	encoded, err := json.Marshal(contract)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("fingerprinting Run Worker pod: %w", err)
	}
	return sha256.Sum256(encoded), nil
}
