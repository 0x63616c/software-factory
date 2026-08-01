package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
)

const (
	envAPIDatabaseURL      = "SOFTWARE_FACTORY_DATABASE_URL"
	envAPIDatabaseUser     = "SOFTWARE_FACTORY_DATABASE_USER"
	envAPIDatabasePassword = "SOFTWARE_FACTORY_DATABASE_PASSWORD"
	envAPIDatabaseHost     = "SOFTWARE_FACTORY_DATABASE_HOST"
	envAPIDatabaseName     = "SOFTWARE_FACTORY_DATABASE_NAME"
	envAPIListenAddr       = "API_ADDR"
	envAPIMetricsAddr      = "METRICS_ADDR"
	envAccessTeamDomain    = "CLOUDFLARE_ACCESS_TEAM_DOMAIN"
	envAccessAudience      = "CLOUDFLARE_ACCESS_AUD"
	envAPIWorkerBearer     = "SOFTWARE_FACTORY_API__WORKER_BEARER_TOKEN"
	envAPIRunWorkerBearer  = "SOFTWARE_FACTORY_API__RUN_WORKER_BEARER_TOKEN"
	envAPITemporalHost     = "TEMPORAL_HOST_PORT"
	envAPITemporalNS       = "TEMPORAL_NAMESPACE"
	envAPIBlobsURL         = "BLOBS_URL"
	envAPIWebhookSecret    = "GITHUB_BOT_APP__WEBHOOK_SECRET"
)

// API is the parsed startup configuration for the factory API process.
type API struct {
	DatabaseURL       string
	ListenAddr        string
	MetricsAddr       string
	LogLevel          slog.Level
	AccessIssuer      string
	AccessAudience    string
	AccessCertsURL    string
	WorkerBearer      string
	RunWorkerBearer   string
	TemporalHostPort  string
	TemporalNamespace string
	BlobsURL          string
	// WebhookSecret is the GitHub App webhook secret internal/webhook verifies
	// deliveries against — the same secret the relay (#535) verifies with,
	// duplicated for the reason internal/webhook's own doc comment gives.
	WebhookSecret []byte
}

// LoadAPI parses all API process configuration before any external work begins.
func LoadAPI() (API, error) {
	teamDomain := os.Getenv(envAccessTeamDomain)
	databaseURL := os.Getenv(envAPIDatabaseURL)
	if strings.TrimSpace(databaseURL) == "" {
		user := os.Getenv(envAPIDatabaseUser)
		password := os.Getenv(envAPIDatabasePassword)
		host := os.Getenv(envAPIDatabaseHost)
		name := os.Getenv(envAPIDatabaseName)
		if user != "" && password != "" && host != "" && name != "" {
			databaseURL = (&url.URL{Scheme: "postgresql", User: url.UserPassword(user, password), Host: host + ":5432", Path: name, RawQuery: "sslmode=disable"}).String()
		}
	}
	cfg := API{
		DatabaseURL:       databaseURL,
		ListenAddr:        os.Getenv(envAPIListenAddr),
		MetricsAddr:       os.Getenv(envAPIMetricsAddr),
		AccessAudience:    os.Getenv(envAccessAudience),
		WorkerBearer:      os.Getenv(envAPIWorkerBearer),
		RunWorkerBearer:   os.Getenv(envAPIRunWorkerBearer),
		TemporalHostPort:  os.Getenv(envAPITemporalHost),
		TemporalNamespace: os.Getenv(envAPITemporalNS),
		BlobsURL:          os.Getenv(envAPIBlobsURL),
		WebhookSecret:     []byte(os.Getenv(envAPIWebhookSecret)),
	}
	for _, required := range []struct{ name, value string }{
		{envAPIDatabaseURL, cfg.DatabaseURL},
		{envAPIListenAddr, cfg.ListenAddr},
		{envAPIMetricsAddr, cfg.MetricsAddr},
		{envAccessTeamDomain, teamDomain},
		{envAccessAudience, cfg.AccessAudience},
		{envAPIWorkerBearer, cfg.WorkerBearer},
		{envAPIRunWorkerBearer, cfg.RunWorkerBearer},
		{envAPITemporalHost, cfg.TemporalHostPort},
		{envAPITemporalNS, cfg.TemporalNamespace},
		{envAPIBlobsURL, cfg.BlobsURL},
		{envAPIWebhookSecret, string(cfg.WebhookSecret)},
	} {
		if strings.TrimSpace(required.value) == "" {
			return API{}, fmt.Errorf("%s is required to start the API", required.name)
		}
	}
	issuer, certsURL, err := accessURLs(teamDomain)
	if err != nil {
		return API{}, err
	}
	cfg.AccessIssuer = issuer
	cfg.AccessCertsURL = certsURL
	level, err := logLevel()
	if err != nil {
		return API{}, err
	}
	cfg.LogLevel = level
	return cfg, nil
}

// accessURLs derives the only Access endpoints we trust from the configured team domain.
// Programmatic callers need a Service Auth policy or Access returns an IdP HTML page. The
// application also needs an Allow policy: Service Auth alone does not reliably include a JWT.
func accessURLs(teamDomain string) (string, string, error) {
	parsed, err := url.Parse("https://" + strings.TrimSpace(teamDomain))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.HasSuffix(parsed.Host, ".cloudflareaccess.com") {
		return "", "", fmt.Errorf("%s must be a Cloudflare Access team domain", envAccessTeamDomain)
	}
	issuer := "https://" + parsed.Host
	return issuer, issuer + "/cdn-cgi/access/certs", nil
}
