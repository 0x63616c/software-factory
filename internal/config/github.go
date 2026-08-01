// Package config reads this service's configuration from its environment, and
// is the only place that may. Every value arrives here once, is turned into a
// typed struct, and is handed to a constructor; nothing downstream reaches for
// an environment variable, so what the service needs to run is enumerable by
// reading this package rather than by grepping for os.Getenv.
package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// GitHub is everything needed to act as the www-software-factory-bot App
// against one repository.
type GitHub struct {
	Owner string
	Repo  string

	AppID          int64
	InstallationID int64

	// PrivateKeyPEM is the App's RSA signing key, already base64-decoded. It is
	// []byte rather than a string, and this struct deliberately has no String
	// or LogValue method, so nothing prints it by accident.
	PrivateKeyPEM []byte
}

// Validate reports whether this config can address a repository as an App.
//
// It exists beside LoadGitHub because a GitHub value can also be built by hand
// — by a test, or by a composition root assembling one from somewhere else —
// and a constructor handed a half-filled struct must fail at construction
// rather than at the first request.
func (g GitHub) Validate() error {
	switch {
	case g.Owner == "":
		return fmt.Errorf("%s is required", envOwner)
	case g.Repo == "":
		return fmt.Errorf("%s is required", envRepo)
	case g.AppID <= 0:
		return fmt.Errorf("%s must be a positive integer, got %d", envAppID, g.AppID)
	case g.InstallationID <= 0:
		return fmt.Errorf("%s must be a positive integer, got %d", envInstallationID, g.InstallationID)
	case len(g.PrivateKeyPEM) == 0:
		return fmt.Errorf("%s is required", envPrivateKeyFile)
	}
	return nil
}

// Environment variables LoadGitHub reads. Named as constants because the error
// messages quote them, and an error naming an input that does not exist is
// worse than no error at all.
const (
	envOwner          = "GITHUB_OWNER"
	envRepo           = "GITHUB_REPO"
	envAppID          = "GITHUB_APP_ID"
	envInstallationID = "GITHUB_APP_INSTALLATION_ID"
	envPrivateKeyFile = "GITHUB_APP_PRIVATE_KEY_PEM_FILE"
)

// LoadGitHub reads the App's identity from the environment and the private key
// from the file the cluster mounts it at.
//
// It fails at startup rather than on first use: a wrong key is a config error,
// and a config error surfacing an hour later inside an activity retry reads as
// a GitHub outage.
func LoadGitHub() (GitHub, error) {
	cfg := GitHub{
		Owner: os.Getenv(envOwner),
		Repo:  os.Getenv(envRepo),
	}

	var err error
	if cfg.Owner == "" {
		return GitHub{}, fmt.Errorf("%s is required: the repository owner this service works tickets for", envOwner)
	}
	if cfg.Repo == "" {
		return GitHub{}, fmt.Errorf("%s is required: the repository this service works tickets for", envRepo)
	}
	if cfg.AppID, err = positiveID(envAppID); err != nil {
		return GitHub{}, err
	}
	if cfg.InstallationID, err = positiveID(envInstallationID); err != nil {
		return GitHub{}, err
	}
	if cfg.PrivateKeyPEM, err = readPrivateKey(); err != nil {
		return GitHub{}, err
	}
	return cfg, nil
}

// positiveID parses one of the two GitHub-assigned identifiers. Zero is
// rejected along with the unparseable, because zero is what a missing value
// would otherwise become and it is not an identifier GitHub ever issues.
func positiveID(name string) (int64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return 0, fmt.Errorf("%s is required: find it in the App's settings", name)
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", name, raw)
	}
	if id <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %d", name, id)
	}
	return id, nil
}

// readPrivateKey reads and decodes the mounted key file.
//
// The vault stores the PEM base64-encoded (scripts/save-github-bot.sh writes
// `.pem | @base64`, so the key survives as a single-line value), and the
// kubelet strips the Secret's own base64 layer on mount. What lands on disk is
// therefore the base64 TEXT of the PEM, not the PEM. Getting that backwards is
// the likeliest first-run failure and its naive symptom — "failed to parse PEM"
// — points at a corrupt key and sends the reader off to rotate a good one, so
// the near miss is diagnosed by name below.
func readPrivateKey() ([]byte, error) {
	path := os.Getenv(envPrivateKeyFile)
	if path == "" {
		return nil, fmt.Errorf("%s is required: the path the App's private key is mounted at", envPrivateKeyFile)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the App private key from %s (%s): %w", path, envPrivateKeyFile, err)
	}

	// The mount adds a trailing newline and wrapped base64 carries interior
	// ones; neither is part of the value.
	encoded := strings.Join(strings.Fields(string(raw)), "")
	if encoded == "" {
		return nil, fmt.Errorf("the App private key file %s (%s) is empty", path, envPrivateKeyFile)
	}
	if strings.HasPrefix(encoded, "-----BEGIN") {
		return nil, fmt.Errorf(
			"the App private key file %s (%s) holds a raw PEM; this input expects the base64-encoded form written by scripts/save-github-bot.sh",
			path, envPrivateKeyFile)
	}

	pem, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decoding the base64 App private key in %s (%s): %w", path, envPrivateKeyFile, err)
	}
	return pem, nil
}
