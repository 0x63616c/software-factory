package k8s

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/0x63616c/software-factory/internal/clients/codexauth"
	"github.com/0x63616c/software-factory/internal/clients/codexauth/codexauthtest"
	"github.com/0x63616c/software-factory/internal/work"
)

const (
	testNamespace = "software-factory"
	testName      = "codex-auth"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(&strings.Builder{}, nil))
}

// newFakeClientset builds a clientset holding one Secret, with resourceVersion
// semantics.
//
// The reactor is the point. client-go's fake ObjectTracker does not advance a
// resourceVersion or enforce one, so without this the fake would accept every
// stale write — and the conformance suite would pass against a store that has
// no compare-and-swap at all. Emulating it here is what lets the suite say
// something about this client rather than about the tracker.
func newFakeClientset(seed map[string][]byte) *fake.Clientset {
	clientset := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: testName, ResourceVersion: "1"},
		Data:       seed,
	})
	version := int64(1)
	clientset.PrependReactor("update", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		update, ok := action.(k8stesting.UpdateAction)
		if !ok {
			return false, nil, nil
		}
		secret, ok := update.GetObject().(*corev1.Secret)
		if !ok {
			return false, nil, nil
		}
		if secret.ResourceVersion != "" && secret.ResourceVersion != strconv.FormatInt(version, 10) {
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Resource: "secrets"}, secret.Name,
				errors.New("the object has been modified; please apply your changes to the latest version"))
		}
		version++
		secret.ResourceVersion = strconv.FormatInt(version, 10)
		return false, nil, nil
	})
	return clientset
}

func newTestClient(t *testing.T, seed map[string][]byte) *SecretClient {
	t.Helper()
	client, err := NewSecretClient(&API{clientset: newFakeClientset(seed)}, testNamespace, testName, testLogger())
	if err != nil {
		t.Fatalf("NewSecretClient: %v", err)
	}
	return client
}

func TestSecretClientSatisfiesTheSecretStoreContract(t *testing.T) {
	t.Parallel()
	// The real store held to the same standard as the fake. A store that
	// applied keys one at a time, or turned a missing precondition into a
	// blind write, would satisfy the Go interface exactly and destroy the
	// credential anyway.
	codexauthtest.RunSecretStoreContract(t, func(t *testing.T, seed map[string][]byte) codexauth.SecretStore {
		return newTestClient(t, seed)
	})
}

func TestSecretClientReturnsEveryKeyAndTheObjectsResourceVersion(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, map[string][]byte{"auth.json": []byte("a"), "refresh_state.json": []byte("s")})

	values, version, err := client.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(values["auth.json"]) != "a" || string(values["refresh_state.json"]) != "s" {
		t.Errorf("values = %v, want both keys", values)
	}
	got, err := version.Precondition()
	if err != nil {
		t.Fatalf("the version returned by Get names no precondition: %v", err)
	}
	if got != "1" {
		t.Errorf("version = %q, want the object's resourceVersion", got)
	}
}

func TestSecretClientReportsAMissingSecretAsNotFound(t *testing.T) {
	t.Parallel()
	client, err := NewSecretClient(&API{clientset: fake.NewClientset()}, testNamespace, testName, testLogger())
	if err != nil {
		t.Fatalf("NewSecretClient: %v", err)
	}

	// Absence means unseeded, which is a human's job. Reporting it as a read
	// failure would retry forever against a secret nobody has created.
	if _, _, err := client.Get(context.Background()); !errors.Is(err, work.ErrSecretNotFound) {
		t.Fatalf("Get returned %v, want work.ErrSecretNotFound", err)
	}
}

func TestSecretClientWritesWithTheCallersResourceVersionAsThePrecondition(t *testing.T) {
	t.Parallel()
	clientset := newFakeClientset(map[string][]byte{"auth.json": []byte("a")})
	client, err := NewSecretClient(&API{clientset: clientset}, testNamespace, testName, testLogger())
	if err != nil {
		t.Fatalf("NewSecretClient: %v", err)
	}

	var sent string
	clientset.PrependReactor("update", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if update, ok := action.(k8stesting.UpdateAction); ok {
			if secret, ok := update.GetObject().(*corev1.Secret); ok {
				sent = secret.ResourceVersion
			}
		}
		return false, nil, nil
	})

	if _, err := client.Put(context.Background(), map[string][]byte{"auth.json": []byte("b")}, work.ObservedVersion("1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// The caller's version, not the freshly read one. A fresh read would be a
	// precondition on nothing and the lease would be gone with nothing to see.
	if sent != "1" {
		t.Errorf("the update carried resourceVersion %q, want the caller's precondition", sent)
	}
}

func TestSecretClientReportsARejectedPreconditionAsAVersionConflict(t *testing.T) {
	t.Parallel()
	clientset := newFakeClientset(map[string][]byte{"auth.json": []byte("a")})
	client, err := NewSecretClient(&API{clientset: clientset}, testNamespace, testName, testLogger())
	if err != nil {
		t.Fatalf("NewSecretClient: %v", err)
	}
	// The apiserver rejecting the precondition is the guarantee that closes
	// the window between this client's read and its update, so its answer must
	// arrive as the domain sentinel rather than as an opaque API error.
	clientset.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, testName, errors.New("modified"))
	})

	_, err = client.Put(context.Background(), map[string][]byte{"auth.json": []byte("b")}, work.ObservedVersion("1"))
	if !errors.Is(err, work.ErrVersionConflict) {
		t.Fatalf("Put returned %v, want work.ErrVersionConflict", err)
	}
}

func TestSecretClientWritesSeveralKeysInOneUpdate(t *testing.T) {
	t.Parallel()
	clientset := newFakeClientset(map[string][]byte{"auth.json": []byte("a"), "untouched": []byte("keep")})
	client, err := NewSecretClient(&API{clientset: clientset}, testNamespace, testName, testLogger())
	if err != nil {
		t.Fatalf("NewSecretClient: %v", err)
	}

	if _, err := client.Put(context.Background(), map[string][]byte{
		"auth.json":          []byte("rotated"),
		"refresh_state.json": []byte("cleared"),
	}, work.ObservedVersion("1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Two updates would put the rotated credential and its lease state at two
	// linearization points, which is the bug the whole seam exists to prevent.
	updates := 0
	for _, action := range clientset.Actions() {
		if action.GetVerb() == "update" {
			updates++
		}
	}
	if updates != 1 {
		t.Errorf("the write took %d updates, want exactly 1", updates)
	}

	values, _, err := client.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for key, want := range map[string]string{"auth.json": "rotated", "refresh_state.json": "cleared", "untouched": "keep"} {
		if got := string(values[key]); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestNewSecretClientRefusesAnIncompleteBinding(t *testing.T) {
	t.Parallel()
	api := &API{clientset: fake.NewClientset()}
	cases := map[string]func() (*SecretClient, error){
		"no API":       func() (*SecretClient, error) { return NewSecretClient(nil, testNamespace, testName, testLogger()) },
		"no namespace": func() (*SecretClient, error) { return NewSecretClient(api, "", testName, testLogger()) },
		"no name":      func() (*SecretClient, error) { return NewSecretClient(api, testNamespace, "", testLogger()) },
		"no logger":    func() (*SecretClient, error) { return NewSecretClient(api, testNamespace, testName, nil) },
	}
	for name, construct := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client, err := construct()
			if err == nil {
				t.Fatal("NewSecretClient returned a usable-but-invalid client, want an error")
			}
			if client != nil {
				t.Error("NewSecretClient returned both a client and an error")
			}
		})
	}
}
