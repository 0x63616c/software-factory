package config

import (
	"strings"
	"testing"
)

func setCompleteAPIEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envAPIDatabaseURL, "postgres://example")
	t.Setenv(envAPIListenAddr, ":8080")
	t.Setenv(envAPIMetricsAddr, ":9090")
	t.Setenv(envAccessTeamDomain, "test.cloudflareaccess.com")
	t.Setenv(envAccessAudience, "test-audience")
	t.Setenv(envAPIWorkerBearer, "test-worker-bearer")
	t.Setenv(envAPIRunWorkerBearer, "test-run-worker-bearer")
	t.Setenv(envAPITemporalHost, "temporal:7233")
	t.Setenv(envAPITemporalNS, "software-factory")
	t.Setenv(envAPIBlobsURL, "http://blobs:8080")
	t.Setenv(envAPIWebhookSecret, "test-webhook-secret")
}

func TestLoadAPIRequiresDatabaseURL(t *testing.T) {
	setCompleteAPIEnv(t)
	t.Setenv(envAPIDatabaseURL, "")
	t.Setenv(envAPIListenAddr, ":8080")
	t.Setenv(envAPIMetricsAddr, ":9090")
	t.Setenv(envAccessTeamDomain, "test.cloudflareaccess.com")
	t.Setenv(envAccessAudience, "test-audience")
	t.Setenv(envAPIWorkerBearer, "test-worker-bearer")
	t.Setenv(envAPIRunWorkerBearer, "test-run-worker-bearer")
	t.Setenv(envAPITemporalHost, "temporal:7233")
	t.Setenv(envAPITemporalNS, "software-factory")
	t.Setenv(envAPIWebhookSecret, "test-webhook-secret")
	if _, err := LoadAPI(); err == nil || !strings.Contains(err.Error(), envAPIDatabaseURL) {
		t.Fatalf("LoadAPI() error = %v, want missing %s", err, envAPIDatabaseURL)
	}
}

func TestLoadAPIRequiresWebhookSecret(t *testing.T) {
	setCompleteAPIEnv(t)
	t.Setenv(envAPIDatabaseURL, "postgres://example")
	t.Setenv(envAPIListenAddr, ":8080")
	t.Setenv(envAPIMetricsAddr, ":9090")
	t.Setenv(envAccessTeamDomain, "test.cloudflareaccess.com")
	t.Setenv(envAccessAudience, "test-audience")
	t.Setenv(envAPIWorkerBearer, "test-worker-bearer")
	t.Setenv(envAPIRunWorkerBearer, "test-run-worker-bearer")
	t.Setenv(envAPITemporalHost, "temporal:7233")
	t.Setenv(envAPITemporalNS, "software-factory")
	t.Setenv(envAPIWebhookSecret, "")
	if _, err := LoadAPI(); err == nil || !strings.Contains(err.Error(), envAPIWebhookSecret) {
		t.Fatalf("LoadAPI() error = %v, want missing %s", err, envAPIWebhookSecret)
	}
}

func TestLoadAPIBuildsDatabaseURLFromCNPGAuthSecretValues(t *testing.T) {
	setCompleteAPIEnv(t)
	t.Setenv(envAPIDatabaseURL, "")
	t.Setenv(envAPIDatabaseUser, "software_factory")
	t.Setenv(envAPIDatabasePassword, "a password/with?characters")
	t.Setenv(envAPIDatabaseHost, "software-factory-postgres-rw")
	t.Setenv(envAPIDatabaseName, "software_factory")
	t.Setenv(envAPIListenAddr, ":8080")
	t.Setenv(envAPIMetricsAddr, ":9090")
	t.Setenv(envAccessTeamDomain, "test.cloudflareaccess.com")
	t.Setenv(envAccessAudience, "test-audience")
	t.Setenv(envAPIWorkerBearer, "test-worker-bearer")
	t.Setenv(envAPIRunWorkerBearer, "test-run-worker-bearer")
	t.Setenv(envAPITemporalHost, "temporal:7233")
	t.Setenv(envAPITemporalNS, "software-factory")
	t.Setenv(envAPIWebhookSecret, "test-webhook-secret")

	cfg, err := LoadAPI()
	if err != nil {
		t.Fatalf("LoadAPI() error = %v", err)
	}
	if got, want := cfg.DatabaseURL, "postgresql://software_factory:a%20password%2Fwith%3Fcharacters@software-factory-postgres-rw:5432/software_factory?sslmode=disable"; got != want {
		t.Fatalf("DatabaseURL = %q, want %q", got, want)
	}
}

func TestLoadAPIParsesAccessEndpoints(t *testing.T) {
	setCompleteAPIEnv(t)
	t.Setenv(envAPIDatabaseURL, "postgres://example")
	t.Setenv(envAPIListenAddr, ":8080")
	t.Setenv(envAPIMetricsAddr, ":9090")
	t.Setenv(envAccessTeamDomain, "test.cloudflareaccess.com")
	t.Setenv(envAccessAudience, "test-audience")
	t.Setenv(envAPIWorkerBearer, "test-worker-bearer")
	t.Setenv(envAPIRunWorkerBearer, "test-run-worker-bearer")
	t.Setenv(envAPITemporalHost, "temporal:7233")
	t.Setenv(envAPITemporalNS, "software-factory")
	t.Setenv(envAPIWebhookSecret, "test-webhook-secret")

	cfg, err := LoadAPI()
	if err != nil {
		t.Fatalf("LoadAPI() error = %v", err)
	}
	if cfg.AccessIssuer != "https://test.cloudflareaccess.com" || cfg.AccessCertsURL != "https://test.cloudflareaccess.com/cdn-cgi/access/certs" {
		t.Fatalf("Access endpoints = (%q, %q), want Cloudflare issuer and certificates URL", cfg.AccessIssuer, cfg.AccessCertsURL)
	}
}

func TestLoadAPIRejectsMalformedAccessDomain(t *testing.T) {
	setCompleteAPIEnv(t)
	t.Setenv(envAPIDatabaseURL, "postgres://example")
	t.Setenv(envAPIListenAddr, ":8080")
	t.Setenv(envAPIMetricsAddr, ":9090")
	t.Setenv(envAccessTeamDomain, "https://not-a-domain")
	t.Setenv(envAccessAudience, "test-audience")
	t.Setenv(envAPIWorkerBearer, "test-worker-bearer")
	t.Setenv(envAPIRunWorkerBearer, "test-run-worker-bearer")
	t.Setenv(envAPITemporalHost, "temporal:7233")
	t.Setenv(envAPITemporalNS, "software-factory")
	t.Setenv(envAPIWebhookSecret, "test-webhook-secret")

	if _, err := LoadAPI(); err == nil || !strings.Contains(err.Error(), envAccessTeamDomain) {
		t.Fatalf("LoadAPI() error = %v, want malformed %s", err, envAccessTeamDomain)
	}
}

func TestLoadAPIRequiresBlobsURL(t *testing.T) {
	setCompleteAPIEnv(t)
	t.Setenv(envAPIBlobsURL, "")
	if _, err := LoadAPI(); err == nil || !strings.Contains(err.Error(), envAPIBlobsURL) {
		t.Fatalf("LoadAPI() error = %v, want missing %s", err, envAPIBlobsURL)
	}
}
