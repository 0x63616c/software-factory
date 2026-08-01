// Package k8s is the only place in this service that speaks to the Kubernetes
// API, and the only place client-go's types exist. Everything it returns is
// domain vocabulary, so no other package inherits a Kubernetes worldview or
// needs a cluster to be tested.
package k8s

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/0x63616c/software-factory/internal/work"
)

// API is this service's connection to the Kubernetes API server.
//
// It is an opaque handle rather than a client-go interface so that no exported
// signature anywhere names a Kubernetes type: the seal holds at the package
// edge, not merely at the import list. Components take one and bind themselves
// to the single object they are allowed to touch.
type API struct {
	clientset kubernetes.Interface
}

// apiTimeout bounds a single request to the API server.
//
// Without it client-go waits indefinitely, so a wedged apiserver hangs a call
// at TCP level rather than failing it. That matters more here than the latency
// suggests: the caller holds a time-bounded lease while it writes, and a write
// that hangs past the lease lets another holder conclude it is dead and
// present a refresh token it has already spent.
const apiTimeout = 30 * time.Second

// NewInClusterAPI connects using the pod's own service account.
func NewInClusterAPI() (*API, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("reading this pod's Kubernetes credentials: %w", err)
	}
	config.Timeout = apiTimeout
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("building a Kubernetes client: %w", err)
	}
	return &API{clientset: clientset}, nil
}

// SecretClient reads and writes one Kubernetes Secret, bound at construction.
//
// One object, chosen once. A caller cannot address a different Secret through
// it, which is what keeps the blast radius of the only component holding a
// refresh token down to a single object — and is why no method takes a
// namespace or a name.
type SecretClient struct {
	api       *API
	namespace string
	name      string
	log       *slog.Logger
}

// NewSecretClient binds a client to exactly one Secret.
func NewSecretClient(api *API, namespace, name string, log *slog.Logger) (*SecretClient, error) {
	switch {
	case api == nil || api.clientset == nil:
		return nil, fmt.Errorf("a secret client needs a Kubernetes API connection")
	case namespace == "" || name == "":
		return nil, fmt.Errorf("a secret client needs the namespace and name of the one secret it may touch")
	case log == nil:
		return nil, fmt.Errorf("a secret client needs a logger")
	}
	return &SecretClient{api: api, namespace: namespace, name: name, log: log}, nil
}

// Get returns every key of the Secret and the version they were read at.
//
// A missing key is not an error here. Which keys must be present, and what
// their absence means, belongs to whoever owns the format — this client knows
// only that the object exists.
func (c *SecretClient) Get(ctx context.Context) (map[string][]byte, work.SecretVersion, error) {
	secret, err := c.api.clientset.CoreV1().Secrets(c.namespace).Get(ctx, c.name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Absence is a signal — the credential has not been seeded — and
			// must never be reported for a transient failure to read, which is
			// retryable and means something entirely different.
			return nil, work.SecretVersion{}, fmt.Errorf("secret %s/%s: %w", c.namespace, c.name, work.ErrSecretNotFound)
		}
		return nil, work.SecretVersion{}, fmt.Errorf("reading secret %s/%s: %w", c.namespace, c.name, err)
	}

	values := make(map[string][]byte, len(secret.Data))
	for k, v := range secret.Data {
		values[k] = append([]byte(nil), v...)
	}
	// Key names, never a value and never a length: a length is a fingerprint.
	c.log.DebugContext(ctx, "read the credential secret",
		"namespace", c.namespace, "name", c.name, "keys", keysOf(values), "resource_version", secret.ResourceVersion)
	return values, work.ObservedVersion(secret.ResourceVersion), nil
}

// Put applies every key of values at one point, and only if the object still
// matches precondition.
//
// It is an Update rather than a server-side Apply because Apply carries no
// resourceVersion precondition: it would look like this, succeed always, and
// silently defeat the lease that the whole credential design rests on.
func (c *SecretClient) Put(ctx context.Context, values map[string][]byte, precondition work.SecretVersion) (work.SecretVersion, error) {
	wanted, err := precondition.Precondition()
	if err != nil {
		return work.SecretVersion{}, fmt.Errorf("writing secret %s/%s: %w", c.namespace, c.name, err)
	}

	secrets := c.api.clientset.CoreV1().Secrets(c.namespace)
	secret, err := secrets.Get(ctx, c.name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return work.SecretVersion{}, fmt.Errorf("writing secret %s/%s: %w", c.namespace, c.name, work.ErrSecretNotFound)
		}
		return work.SecretVersion{}, fmt.Errorf("reading secret %s/%s before writing it: %w", c.namespace, c.name, err)
	}
	// The precondition is checked here as well as by the apiserver. Both are
	// wanted: this one catches a stale write without a round trip and holds
	// even against a client whose conflict handling is untested, while the
	// apiserver's is the only thing that closes the window between this read
	// and the update below.
	if wanted != "" && secret.ResourceVersion != wanted {
		return work.SecretVersion{}, fmt.Errorf("writing secret %s/%s: %w", c.namespace, c.name, work.ErrVersionConflict)
	}

	if secret.Data == nil {
		secret.Data = make(map[string][]byte, len(values))
	}
	for k, v := range values {
		secret.Data[k] = append([]byte(nil), v...)
	}
	// The caller's version, not the one just read. The caller's is the
	// precondition; the fresh one would be a precondition on nothing. An empty
	// string here is Kubernetes' spelling of "overwrite blind", which only
	// work.Unconditional can produce.
	secret.ResourceVersion = wanted

	updated, err := secrets.Update(ctx, secret, metav1.UpdateOptions{})
	if err != nil {
		if apierrors.IsConflict(err) {
			return work.SecretVersion{}, fmt.Errorf("writing secret %s/%s: %w", c.namespace, c.name, work.ErrVersionConflict)
		}
		return work.SecretVersion{}, fmt.Errorf("writing secret %s/%s: %w", c.namespace, c.name, err)
	}
	c.log.InfoContext(ctx, "wrote the credential secret",
		"namespace", c.namespace, "name", c.name, "keys", keysOf(values), "resource_version", updated.ResourceVersion)
	return work.ObservedVersion(updated.ResourceVersion), nil
}

// keysOf names the keys of a write for a log line. The names are safe; the
// values are not, and neither are their lengths.
func keysOf(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	return keys
}
