package k8s

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/0x63616c/software-factory/internal/work"
)

func validRunWorkerSpec() work.RunWorkerSpec {
	identity, err := work.NewRunWorkerIdentity("019fb900-0000-7000-8000-000000000001", 1)
	if err != nil {
		panic(err)
	}
	spec, err := work.NewRunWorkerSpec(work.RunWorkerSpec{
		TicketNumber:    42,
		Identity:        identity,
		Image:           "ghcr.io/0x63616c/www-software-factory-run-worker@sha256:" + strings.Repeat("a", 64),
		CPURequest:      "2",
		MemoryLimit:     "8Gi",
		DeadlineSeconds: 86400,
		Env: map[string]string{
			work.GhConfigDirEnv:                work.GhConfigDir,
			work.RunWorkerBranchEnv:            "software-factory/ticket-42/run",
			work.RunWorkerTemporalHostPortEnv:  "temporal-frontend.temporal:7233",
			work.RunWorkerTemporalNamespaceEnv: "software-factory",
			work.RunWorkerBlobsURLEnv:          "http://software-factory-blobs:8080",
			work.RunWorkerMetricsAddrEnv:       ":9090",
			work.RunWorkerCheckpointAPIURLEnv:  "http://software-factory-api:8080",
			work.RunWorkerGitHubRepositoryEnv:  "0x63616c/world-wide-webb",
		},
	})
	if err != nil {
		panic(err)
	}
	return spec
}

func mustBuildRunWorker(t *testing.T) *corev1.Pod {
	t.Helper()
	pod, err := buildRunWorkerPod(validRunWorkerSpec(), runWorkerOptions{})
	if err != nil {
		t.Fatalf("buildRunWorkerPod: %v", err)
	}
	return pod
}

func runWorkerContainer(t *testing.T, pod *corev1.Pod, name string) corev1.Container {
	t.Helper()
	for _, container := range pod.Spec.Containers {
		if container.Name == name {
			return container
		}
	}
	t.Fatalf("Run Worker pod has no %q container", name)
	return corev1.Container{}
}

func TestRunWorkerPodMatchCoversTheAuthoritativeSecurityAndRuntimeSpec(t *testing.T) {
	t.Parallel()

	want := mustBuildRunWorker(t)
	mutations := map[string]func(*corev1.Pod){
		"labels":                func(p *corev1.Pod) { p.Labels[labelRunID] = "another-run" },
		"deadline":              func(p *corev1.Pod) { p.Spec.ActiveDeadlineSeconds = ptr(int64(1)) },
		"service account token": func(p *corev1.Pod) { p.Spec.AutomountServiceAccountToken = ptr(true) },
		"pod security context":  func(p *corev1.Pod) { p.Spec.SecurityContext.FSGroup = ptr(int64(2000)) },
		"projected volumes":     func(p *corev1.Pod) { p.Spec.Volumes[1].Projected.Sources[0].Secret.Name = "foreign" },
		"resources":             func(p *corev1.Pod) { p.Spec.Containers[0].Resources.Limits = nil },
		"container security":    func(p *corev1.Pod) { p.Spec.Containers[0].SecurityContext.AllowPrivilegeEscalation = ptr(true) },
		"image":                 func(p *corev1.Pod) { p.Spec.Containers[0].Image += "-different" },
		"command":               func(p *corev1.Pod) { p.Spec.Containers[0].Command = []string{"sleep"} },
		"environment":           func(p *corev1.Pod) { p.Spec.Containers[0].Env[0].Value = "different" },
	}
	for name, mutate := range mutations {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := want.DeepCopy()
			mutate(got)
			if runWorkerPodMatches(got, want) {
				t.Fatalf("pod drift in %s was accepted", name)
			}
		})
	}
	if !runWorkerPodMatches(want.DeepCopy(), want) {
		t.Fatal("identical authoritative pod specs did not match")
	}
}

func TestRunWorkerPodHasOneGenerationSpecificIdentityAndQueue(t *testing.T) {
	t.Parallel()
	pod := mustBuildRunWorker(t)
	if pod.Name != "run-worker-019fb900-0000-7000-8000-000000000001-g1" {
		t.Errorf("name = %q", pod.Name)
	}
	c := pod.Spec.Containers[0]
	if !reflect.DeepEqual(c.Command, []string{"/usr/local/bin/run-worker"}) || len(c.Args) != 0 {
		t.Errorf("command = %v args = %v", c.Command, c.Args)
	}
	env := map[string]string{}
	for _, item := range c.Env {
		env[item.Name] = item.Value
		if item.ValueFrom != nil {
			t.Errorf("env %s reads a secret/value source", item.Name)
		}
	}
	wantQueue, err := work.RunWorkerTaskQueue(validRunWorkerSpec().Identity)
	if err != nil {
		t.Fatal(err)
	}
	if env[work.RunWorkerTaskQueueEnv] != wantQueue {
		t.Errorf("private queue = %q", env[work.RunWorkerTaskQueueEnv])
	}
	if env[work.RunWorkerIDEnv] != pod.Name || env[work.RunWorkerGenerationEnv] != "1" {
		t.Errorf("identity env = %+v", env)
	}
	for _, forbidden := range []string{"DATABASE_URL", "GITHUB_APP_PRIVATE_KEY", "GITHUB_TOKEN", "GH_TOKEN", "CHECKPOINT_CAPABILITY"} {
		if _, ok := env[forbidden]; ok {
			t.Errorf("secret-bearing env %s is present", forbidden)
		}
	}
}

func TestRunWorkerPodProjectsEveryUpdateableSecretAsADirectory(t *testing.T) {
	t.Parallel()
	pod := mustBuildRunWorker(t)
	want := map[string]string{
		runWorkerGitHubVolumeName:     work.RunWorkerGitHubCredentialDir,
		runWorkerCheckpointVolumeName: work.RunWorkerCheckpointCapabilityDir,
	}
	for _, mount := range pod.Spec.Containers[0].VolumeMounts {
		path, ok := want[mount.Name]
		if !ok {
			continue
		}
		if mount.MountPath != path {
			t.Errorf("mount %s path = %q, want %q", mount.Name, mount.MountPath, path)
		}
		if mount.SubPath != "" {
			t.Errorf("mount %s uses subPath %q; projected updates would freeze", mount.Name, mount.SubPath)
		}
		if !mount.ReadOnly {
			t.Errorf("mount %s is writable", mount.Name)
		}
		delete(want, mount.Name)
	}
	if len(want) != 0 {
		t.Errorf("missing projected directories: %v", want)
	}
}

func TestRunWorkerPodHasWritableWorkAndNoKubernetesCredential(t *testing.T) {
	t.Parallel()
	pod := mustBuildRunWorker(t)
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Errorf("automountServiceAccountToken = %v", pod.Spec.AutomountServiceAccountToken)
	}
	if pod.Spec.ServiceAccountName != "" {
		t.Errorf("serviceAccountName = %q", pod.Spec.ServiceAccountName)
	}
	foundWork := false
	for _, mount := range pod.Spec.Containers[0].VolumeMounts {
		if mount.MountPath == work.WorkspaceRoot {
			foundWork = true
			if mount.ReadOnly {
				t.Error("/work is read-only")
			}
		}
	}
	if !foundWork {
		t.Error("/work is not mounted")
	}
}

func TestRunWorkerPodExposesToolMetricsWithoutCodexCredentialProjection(t *testing.T) {
	t.Parallel()

	pod := mustBuildRunWorker(t)
	if pod.Annotations["prometheus.io/scrape"] != "true" || pod.Annotations["prometheus.io/port"] != "9090" || pod.Annotations["prometheus.io/path"] != "/metrics" {
		t.Fatalf("metrics scrape annotations = %#v", pod.Annotations)
	}
	container := pod.Spec.Containers[0]
	if !reflect.DeepEqual(container.Ports, []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9090}}) {
		t.Fatalf("metrics ports = %#v", container.Ports)
	}
	for _, env := range container.Env {
		if env.Name == "CODEX_HOME" {
			t.Fatal("Run Worker still publishes CODEX_HOME")
		}
	}
	for _, mount := range container.VolumeMounts {
		if strings.Contains(strings.ToLower(mount.Name+" "+mount.MountPath), "codex") {
			t.Fatalf("Run Worker still mounts a Codex credential: %#v", mount)
		}
	}
	for _, volume := range pod.Spec.Volumes {
		if strings.Contains(strings.ToLower(volume.Name), "codex") {
			t.Fatalf("Run Worker still projects a Codex credential: %#v", volume)
		}
	}
}

func TestRunWorkerPodSeparatesCredentialFreeToolsFromRepositoryActivities(t *testing.T) {
	t.Parallel()
	pod := mustBuildRunWorker(t)
	tool := runWorkerContainer(t, pod, "tool-worker")
	if !reflect.DeepEqual(tool.Command, []string{"/usr/local/bin/tool-worker"}) {
		t.Fatalf("tool command = %#v", tool.Command)
	}
	if len(tool.VolumeMounts) != 1 || tool.VolumeMounts[0].Name != workVolumeName || tool.VolumeMounts[0].MountPath != work.WorkspaceRoot {
		t.Fatalf("tool mounts = %#v, want shared /work only", tool.VolumeMounts)
	}
	env := map[string]string{}
	for _, item := range tool.Env {
		env[item.Name] = item.Value
	}
	wantQueue, err := work.RunWorkerToolTaskQueue(validRunWorkerSpec().Identity)
	if err != nil {
		t.Fatal(err)
	}
	if env[work.ToolWorkerTaskQueueEnv] != wantQueue {
		t.Fatalf("tool queue = %q, want %q", env[work.ToolWorkerTaskQueueEnv], wantQueue)
	}
	if _, exists := env["SANDBOX_TASK_QUEUE"]; exists {
		t.Fatal("target Tool Worker still publishes the retired SANDBOX_TASK_QUEUE name")
	}
	for _, forbidden := range []string{work.RunWorkerGitHubCredentialDir, work.RunWorkerCheckpointCapabilityDir, work.RunWorkerRepositoryCapabilityDir} {
		for _, mount := range tool.VolumeMounts {
			if mount.MountPath == forbidden {
				t.Fatalf("tool container mounts credential path %q", forbidden)
			}
		}
	}
	repository := runWorkerContainer(t, pod, runWorkerContainerName)
	if len(repository.VolumeMounts) <= 1 {
		t.Fatalf("repository container has no credential mounts: %#v", repository.VolumeMounts)
	}
}
