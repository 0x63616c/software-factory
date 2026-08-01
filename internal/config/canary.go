package config

import (
	"fmt"
	"os"
)

const (
	envCanaryResponsesEndpoint    = "CODEX_RESPONSES_ENDPOINT"
	envCanaryResponsesAccessToken = "CODEX_RESPONSES_ACCESS_TOKEN"
	envCanaryResponsesAccountID   = "CODEX_RESPONSES_ACCOUNT_ID"
	envCanaryResponsesModel       = "CODEX_RESPONSES_MODEL"
)

// CanaryResponses is the deliberately separate configuration for a manual
// call to the real Responses service. It is never loaded by a production
// service or the deterministic E2E suite.
type CanaryResponses struct {
	Endpoint    string
	AccessToken string
	AccountID   string
	Model       string
}

// LoadCanaryResponses loads the credentials and target for the opt-in canary.
func LoadCanaryResponses() (CanaryResponses, error) {
	result := CanaryResponses{
		Endpoint:    os.Getenv(envCanaryResponsesEndpoint),
		AccessToken: os.Getenv(envCanaryResponsesAccessToken),
		AccountID:   os.Getenv(envCanaryResponsesAccountID),
		Model:       os.Getenv(envCanaryResponsesModel),
	}
	for name, value := range map[string]string{
		envCanaryResponsesEndpoint:    result.Endpoint,
		envCanaryResponsesAccessToken: result.AccessToken,
		envCanaryResponsesAccountID:   result.AccountID,
		envCanaryResponsesModel:       result.Model,
	} {
		if value == "" {
			return CanaryResponses{}, fmt.Errorf("%s is required for the Responses canary", name)
		}
	}
	return result, nil
}
