package config

import (
	"os"
	"strings"
)

// PayloadCodecModeEnv is the environment variable selecting the Temporal payload codec mode.
const PayloadCodecModeEnv = "PAYLOAD_CODEC_MODE"

// PayloadCodecMode returns the configured Temporal payload codec mode.
func PayloadCodecMode() string {
	return strings.TrimSpace(os.Getenv(PayloadCodecModeEnv))
}
