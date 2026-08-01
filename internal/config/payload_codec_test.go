package config

import "testing"

func TestPayloadCodecModeReadsTheEnvironment(t *testing.T) {
	t.Setenv(PayloadCodecModeEnv, " decode-only ")

	if got := PayloadCodecMode(); got != "decode-only" {
		t.Errorf("PayloadCodecMode() = %q, want decode-only", got)
	}
}
