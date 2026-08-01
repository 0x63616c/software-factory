package k8s

import (
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/0x63616c/software-factory/internal/work"
)

const (
	runWorkerBinaryPath                 = "/usr/local/bin/run-worker"
	runWorkerToolBinaryPath             = "/usr/local/bin/tool-worker"
	runWorkerContainerName              = "run-worker"
	runWorkerToolContainerName          = "tool-worker"
	runWorkerUID                  int64 = 1000
	runWorkerGitHubVolumeName           = "github-credential"
	runWorkerCheckpointVolumeName       = "checkpoint-capability"
	runWorkerRepositoryVolumeName       = "repository-checkpoint-capability"
)

var runWorkerSecretMode int32 = 0o440

var allowedRunWorkerEnvKeys = map[string]bool{
	work.GhConfigDirEnv:                true,
	work.RunWorkerBranchEnv:            true,
	work.RunWorkerTemporalHostPortEnv:  true,
	work.RunWorkerTemporalNamespaceEnv: true,
	work.RunWorkerBlobsURLEnv:          true,
	work.RunWorkerMetricsAddrEnv:       true,
	work.RunWorkerCheckpointAPIURLEnv:  true,
	work.RunWorkerGitHubRepositoryEnv:  true,
}

// buildRunWorkerPod is the pure target pod renderer. The legacy buildPod
// remains unchanged until quiesced activation.
func buildRunWorkerPod(spec work.RunWorkerSpec, o runWorkerOptions) (*corev1.Pod, error) {
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("building Run Worker pod: %w", err)
	}
	id, err := work.RunWorkerName(spec.Identity)
	if err != nil {
		return nil, fmt.Errorf("building Run Worker pod name: %w", err)
	}
	if problems := validation.IsDNS1123Label(string(id)); len(problems) > 0 {
		return nil, fmt.Errorf("run worker name %q is invalid: %s: %w", id, problems[0], work.ErrPermanent)
	}
	if !isPinnedImage(spec.Image) {
		return nil, fmt.Errorf("run worker image %q is not pinned by sha256 digest: %w", spec.Image, work.ErrPermanent)
	}
	if spec.TicketNumber <= 0 || spec.DeadlineSeconds <= 0 {
		return nil, fmt.Errorf("run worker needs a positive Ticket and deadline: %w", work.ErrPermanent)
	}
	for key := range spec.Env {
		if !allowedRunWorkerEnvKeys[key] {
			return nil, fmt.Errorf("run worker env %q is not allowlisted: %w", key, work.ErrPermanent)
		}
	}
	cpu, err := resource.ParseQuantity(spec.CPURequest)
	if err != nil {
		return nil, fmt.Errorf("run worker CPU request %q: %w: %w", spec.CPURequest, err, work.ErrPermanent)
	}
	memory, err := resource.ParseQuantity(spec.MemoryLimit)
	if err != nil {
		return nil, fmt.Errorf("run worker memory limit %q: %w: %w", spec.MemoryLimit, err, work.ErrPermanent)
	}

	env := make(map[string]string, len(spec.Env)+5)
	for key, value := range spec.Env {
		env[key] = value
	}
	env[work.RunWorkerIDEnv] = string(id)
	env[work.RunWorkerRunIDEnv] = spec.Identity.RunID
	env[work.RunWorkerGenerationEnv] = strconv.Itoa(spec.Identity.Generation)
	taskQueue, err := work.RunWorkerTaskQueue(spec.Identity)
	if err != nil {
		return nil, fmt.Errorf("building Run Worker pod task queue: %w", err)
	}
	env[work.RunWorkerTaskQueueEnv] = taskQueue
	toolTaskQueue, err := work.RunWorkerToolTaskQueue(spec.Identity)
	if err != nil {
		return nil, fmt.Errorf("building Run Worker tool task queue: %w", err)
	}
	toolEnv := map[string]string{
		work.ToolWorkerTemporalHostPortEnv:  spec.Env[work.RunWorkerTemporalHostPortEnv],
		work.ToolWorkerTemporalNamespaceEnv: spec.Env[work.RunWorkerTemporalNamespaceEnv],
		work.ToolWorkerTaskQueueEnv:         toolTaskQueue,
		work.ToolWorkerBlobsURLEnv:          spec.Env[work.RunWorkerBlobsURLEnv],
	}

	uid := runWorkerUID
	workSize := resource.NewQuantity(workSizeLimitBytes, resource.BinarySI)
	pullSecrets := []corev1.LocalObjectReference(nil)
	if o.imagePullSecretName != "" {
		pullSecrets = []corev1.LocalObjectReference{{Name: o.imagePullSecretName}}
	}
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: memory},
	}
	repositoryResources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
	}
	labels := runWorkerLabels(spec)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: string(id), Labels: labels, Annotations: map[string]string{
			"prometheus.io/scrape": "true", "prometheus.io/port": "9090", "prometheus.io/path": "/metrics",
		}},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			ActiveDeadlineSeconds:         ptr(spec.DeadlineSeconds),
			AutomountServiceAccountToken:  ptr(false),
			EnableServiceLinks:            ptr(false),
			TerminationGracePeriodSeconds: ptr(int64(90)),
			SecurityContext:               &corev1.PodSecurityContext{FSGroup: &uid},
			ImagePullSecrets:              pullSecrets,
			Containers: []corev1.Container{{
				Name:            runWorkerContainerName,
				Image:           spec.Image,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{runWorkerBinaryPath},
				Env:             sortedEnv(env),
				Ports:           []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9090}},
				Resources:       repositoryResources,
				VolumeMounts: []corev1.VolumeMount{
					{Name: workVolumeName, MountPath: work.WorkspaceRoot},
					{Name: runWorkerGitHubVolumeName, MountPath: work.RunWorkerGitHubCredentialDir, ReadOnly: true},
					{Name: runWorkerCheckpointVolumeName, MountPath: work.RunWorkerCheckpointCapabilityDir, ReadOnly: true},
					{Name: runWorkerRepositoryVolumeName, MountPath: work.RunWorkerRepositoryCapabilityDir, ReadOnly: true},
				},
				SecurityContext: &corev1.SecurityContext{
					RunAsNonRoot:             ptr(true),
					RunAsUser:                &uid,
					RunAsGroup:               &uid,
					AllowPrivilegeEscalation: ptr(false),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				},
			}, {
				Name: runWorkerToolContainerName, Image: spec.Image, ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{runWorkerToolBinaryPath}, Env: sortedEnv(toolEnv), Resources: resources,
				VolumeMounts: []corev1.VolumeMount{{Name: workVolumeName, MountPath: work.WorkspaceRoot}},
				SecurityContext: &corev1.SecurityContext{
					RunAsNonRoot: ptr(true), RunAsUser: &uid, RunAsGroup: &uid,
					AllowPrivilegeEscalation: ptr(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				},
			}},
			Volumes: []corev1.Volume{
				{Name: workVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: workSize}}},
				projectedSecretVolume(runWorkerGitHubVolumeName, runWorkerGitHubSecretName(id)),
				projectedSecretVolume(runWorkerCheckpointVolumeName, runWorkerCheckpointSecretName(id)),
				projectedSecretVolume(runWorkerRepositoryVolumeName, runWorkerRepositorySecretName(id)),
			},
		},
	}, nil
}

func isPinnedImage(image string) bool {
	marker := "@sha256:"
	i := strings.LastIndex(image, marker)
	if i <= 0 || len(image[i+len(marker):]) != 64 {
		return false
	}
	return strings.Trim(image[i+len(marker):], "0123456789abcdef") == ""
}

func projectedSecretVolume(volumeName, secretName string) corev1.Volume {
	return corev1.Volume{Name: volumeName, VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
		DefaultMode: &runWorkerSecretMode,
		Sources:     []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: secretName}}}},
	}}}
}

func runWorkerLabels(spec work.RunWorkerSpec) map[string]string {
	return map[string]string{
		labelName:       "software-factory-run-worker",
		labelManagedBy:  labelManagedByValue,
		labelTicket:     strconv.Itoa(spec.TicketNumber),
		labelRunID:      spec.Identity.RunID,
		labelGeneration: strconv.Itoa(spec.Identity.Generation),
	}
}
