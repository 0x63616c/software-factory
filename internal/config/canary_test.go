package config

import "testing"

func TestLoadCanaryResponsesRequiresEveryOptInValue(t *testing.T) {
	t.Setenv(envCanaryResponsesEndpoint, "https://example.test/responses")
	t.Setenv(envCanaryResponsesAccessToken, "secret")
	t.Setenv(envCanaryResponsesAccountID, "account")
	t.Setenv(envCanaryResponsesModel, "")

	if _, err := LoadCanaryResponses(); err == nil {
		t.Fatal("LoadCanaryResponses() error = nil, want missing model error")
	}
}

func TestLoadCanaryResponsesReturnsConfiguredTarget(t *testing.T) {
	t.Setenv(envCanaryResponsesEndpoint, "https://example.test/responses")
	t.Setenv(envCanaryResponsesAccessToken, "secret")
	t.Setenv(envCanaryResponsesAccountID, "account")
	t.Setenv(envCanaryResponsesModel, "gpt-canary")

	got, err := LoadCanaryResponses()
	if err != nil {
		t.Fatalf("LoadCanaryResponses() error = %v", err)
	}
	if got.Endpoint != "https://example.test/responses" || got.Model != "gpt-canary" {
		t.Fatalf("LoadCanaryResponses() = %#v", got)
	}
}
