package k8s

import (
	"fmt"
	"log/slog"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// RunWorkers is the target-only Kubernetes capability. Unlike Sandboxes it
// deliberately has no exec, clone, or remote file-transfer surface.
type RunWorkers struct {
	cs     kubernetes.Interface
	ns     string
	logger *slog.Logger
	opts   runWorkerOptions
}

type runWorkerOptions struct{ imagePullSecretName string }

// NewRunWorkersInCluster binds target worker lifecycle to one namespace.
func NewRunWorkersInCluster(namespace string, logger *slog.Logger, imagePullSecretName string) (*RunWorkers, error) {
	o, err := resolveRunWorkerOptions(imagePullSecretName)
	if err != nil {
		return nil, fmt.Errorf("constructing Run Workers: %w", err)
	}
	if err := validateRunWorkerDeps(namespace, logger); err != nil {
		return nil, fmt.Errorf("constructing Run Workers: %w", err)
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("reading in-cluster Kubernetes configuration for Run Workers: %w", err)
	}
	cfg.Timeout = apiTimeout
	cfg.WarningHandlerWithContext = warningLogger{logger: logger}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building the Run Worker Kubernetes client: %w", err)
	}
	return &RunWorkers{cs: cs, ns: namespace, logger: logger, opts: o}, nil
}

func newRunWorkers(cs kubernetes.Interface, namespace string, logger *slog.Logger, imagePullSecretName string) (*RunWorkers, error) {
	o, err := resolveRunWorkerOptions(imagePullSecretName)
	if err != nil {
		return nil, fmt.Errorf("constructing Run Workers: %w", err)
	}
	if err := validateRunWorkerDeps(namespace, logger); err != nil {
		return nil, fmt.Errorf("constructing Run Workers: %w", err)
	}
	if cs == nil {
		return nil, fmt.Errorf("constructing RunWorkers: the clientset is nil")
	}
	return &RunWorkers{cs: cs, ns: namespace, logger: logger, opts: o}, nil
}

func resolveRunWorkerOptions(imagePullSecretName string) (runWorkerOptions, error) {
	if imagePullSecretName != "" {
		if problems := validation.IsDNS1123Subdomain(imagePullSecretName); len(problems) > 0 {
			return runWorkerOptions{}, fmt.Errorf("image pull secret %q is invalid: %s", imagePullSecretName, problems[0])
		}
	}
	return runWorkerOptions{imagePullSecretName: imagePullSecretName}, nil
}

func validateRunWorkerDeps(namespace string, logger *slog.Logger) error {
	if problems := validation.IsDNS1123Label(namespace); namespace == "" || len(problems) > 0 {
		return fmt.Errorf("namespace %q is invalid", namespace)
	}
	if logger == nil {
		return fmt.Errorf("a logger is required")
	}
	return nil
}

func ignoreAbsent(err error) error {
	if err == nil || apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
