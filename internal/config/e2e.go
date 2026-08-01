package config

import "os"

// E2EResultPathEnv is the optional machine-checkable acceptance artifact path.
const E2EResultPathEnv = "SOFTWARE_FACTORY_E2E_RESULT"

// ScenarioTracePathEnv is the optional deterministic scenario trace to replay.
const ScenarioTracePathEnv = "SOFTWARE_FACTORY_SCENARIO_TRACE"

// E2EResultPath returns the optional E2E acceptance artifact path.
func E2EResultPath() string { return os.Getenv(E2EResultPathEnv) }

// ScenarioTracePath returns the optional deterministic scenario trace to replay.
func ScenarioTracePath() string { return os.Getenv(ScenarioTracePathEnv) }
