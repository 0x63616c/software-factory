package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// testPEM is generated once per test binary rather than checked in: a file
// shaped like a private key trips secret scanners and reads to a human as a
// live credential, whichever comment sits above it.
var testPEM = sync.OnceValue(func() string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("generating a test key: " + err.Error())
	}
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
})

// setGitHubEnv sets every input LoadGitHub reads. Tests override or unset one
// at a time from this valid baseline, so a failure names the input under test.
func setGitHubEnv(t *testing.T, keyFile string) {
	t.Helper()
	t.Setenv("GITHUB_OWNER", "0x63616c")
	t.Setenv("GITHUB_REPO", "world-wide-webb")
	t.Setenv("GITHUB_APP_ID", "1234567")
	t.Setenv("GITHUB_APP_INSTALLATION_ID", "89012345")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_PEM_FILE", keyFile)
}

// writeKeyFile writes content to a file in the test's own directory, standing
// in for the /run/secrets mount the worker reads in the cluster.
func writeKeyFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "GITHUB_BOT_APP__PRIVATE_KEY_PEM")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing the test key file: %v", err)
	}
	return path
}

func TestLoadGitHubDecodesABase64PrivateKeyFromItsMountedFile(t *testing.T) {
	setGitHubEnv(t, writeKeyFile(t, base64.StdEncoding.EncodeToString([]byte(testPEM()))))

	got, err := LoadGitHub()
	if err != nil {
		t.Fatalf("LoadGitHub returned an unexpected error: %v", err)
	}
	if string(got.PrivateKeyPEM) != testPEM() {
		t.Errorf("PrivateKeyPEM = %q, want the decoded pem", got.PrivateKeyPEM)
	}
	if got.Owner != "0x63616c" || got.Repo != "world-wide-webb" {
		t.Errorf("repository = %s/%s, want 0x63616c/world-wide-webb", got.Owner, got.Repo)
	}
	if got.AppID != 1234567 || got.InstallationID != 89012345 {
		t.Errorf("ids = app %d installation %d, want 1234567 and 89012345", got.AppID, got.InstallationID)
	}
}

func TestLoadGitHubToleratesTheWhitespaceASecretMountAdds(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(testPEM()))
	wrapped := encoded[:20] + "\n" + encoded[20:40] + "\n" + encoded[40:] + "\n"
	setGitHubEnv(t, writeKeyFile(t, wrapped))

	got, err := LoadGitHub()
	if err != nil {
		t.Fatalf("LoadGitHub returned an unexpected error: %v", err)
	}
	if string(got.PrivateKeyPEM) != testPEM() {
		t.Errorf("PrivateKeyPEM = %q, want the decoded pem", got.PrivateKeyPEM)
	}
}

func TestLoadGitHubNamesTheInputWhenTheKeyFileHoldsARawPemInsteadOfBase64(t *testing.T) {
	setGitHubEnv(t, writeKeyFile(t, testPEM()))

	_, err := LoadGitHub()
	if err == nil {
		t.Fatal("LoadGitHub accepted a raw pem where the base64 form is expected")
	}
	msg := err.Error()
	if !strings.Contains(msg, "GITHUB_APP_PRIVATE_KEY_PEM_FILE") {
		t.Errorf("error %q does not name the input that is wrong", msg)
	}
	if !strings.Contains(msg, "raw PEM") {
		t.Errorf("error %q does not say the file holds a raw PEM", msg)
	}
	if strings.Contains(msg, "illegal base64") {
		t.Errorf("error %q leads with a base64 decode failure, which reads as a corrupt key", msg)
	}
}

func TestLoadGitHubFailsWhenTheKeyFileIsAbsent(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nothing-here")
	setGitHubEnv(t, missing)

	_, err := LoadGitHub()
	if err == nil {
		t.Fatal("LoadGitHub accepted a path that does not exist")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q does not name the path it could not read", err)
	}
}

func TestLoadGitHubFailsWhenARequiredInputIsMissing(t *testing.T) {
	for _, name := range []string{
		"GITHUB_OWNER",
		"GITHUB_REPO",
		"GITHUB_APP_ID",
		"GITHUB_APP_INSTALLATION_ID",
		"GITHUB_APP_PRIVATE_KEY_PEM_FILE",
	} {
		t.Run(name, func(t *testing.T) {
			setGitHubEnv(t, writeKeyFile(t, base64.StdEncoding.EncodeToString([]byte(testPEM()))))
			t.Setenv(name, "")

			_, err := LoadGitHub()
			if err == nil {
				t.Fatalf("LoadGitHub accepted an empty %s", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name the missing input %s", err, name)
			}
		})
	}
}

func TestLoadGitHubFailsWhenAnIDIsNotAPositiveInteger(t *testing.T) {
	for _, name := range []string{"GITHUB_APP_ID", "GITHUB_APP_INSTALLATION_ID"} {
		for _, value := range []string{"not-a-number", "0", "-1", "12.5"} {
			t.Run(name+"="+value, func(t *testing.T) {
				setGitHubEnv(t, writeKeyFile(t, base64.StdEncoding.EncodeToString([]byte(testPEM()))))
				t.Setenv(name, value)

				_, err := LoadGitHub()
				if err == nil {
					t.Fatalf("LoadGitHub accepted %s=%s", name, value)
				}
				if !strings.Contains(err.Error(), name) {
					t.Errorf("error %q does not name the input %s", err, name)
				}
			})
		}
	}
}
