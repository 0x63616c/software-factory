package k8s

import (
	"os"
	"strings"
	"testing"
)

func TestRunWorkerImageShipsTheToolWorkerCommandUsedByThePod(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("../../../images/run-worker/Dockerfile")
	if err != nil {
		t.Fatalf("read Run Worker Dockerfile: %v", err)
	}
	dockerfile := string(body)
	for _, required := range []string{"./cmd/tool-worker", "/out/tool-worker", "/usr/local/bin/tool-worker"} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("Run Worker Dockerfile does not contain %q", required)
		}
	}
	if strings.Contains(dockerfile, "sandbox-worker") {
		t.Error("Run Worker Dockerfile still packages the retired sandbox-worker command")
	}
}

func TestRunWorkerSmokeIsSelfContainedAndWorkerImageDropsFactoryCTL(t *testing.T) {
	t.Parallel()
	smoke, err := os.ReadFile("../../../images/run-worker/smoke.sh")
	if err != nil {
		t.Fatalf("read Run Worker smoke test: %v", err)
	}
	if strings.Contains(string(smoke), "../sandbox/") {
		t.Error("Run Worker smoke test still depends on the deleted sandbox image directory")
	}
	for _, required := range []string{"tool-worker", "Playwright Chromium", "Kubernetes-shaped mount"} {
		if !strings.Contains(string(smoke), required) {
			t.Errorf("Run Worker smoke test does not assert %q", required)
		}
	}

	workerDockerfile, err := os.ReadFile("../../../images/worker/Dockerfile")
	if err != nil {
		t.Fatalf("read worker Dockerfile: %v", err)
	}
	if strings.Contains(string(workerDockerfile), "factoryctl") {
		t.Error("worker Dockerfile still builds or ships the retired factoryctl command")
	}
}
