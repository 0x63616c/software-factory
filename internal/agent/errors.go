package agent

const (
	// ErrorTypeInvalidInput identifies a non-retryable agent activity contract violation.
	ErrorTypeInvalidInput = "InvalidAgentInput"
	// ErrorTypeInvalidProviderOutcome identifies an incomplete or unsupported provider result.
	ErrorTypeInvalidProviderOutcome = "InvalidProviderOutcome"
	// ErrorTypeRateLimit identifies provider capacity that should trip the factory breaker.
	ErrorTypeRateLimit = "RateLimit"
	// ErrorTypeAuth identifies a provider credential that requires operator repair.
	ErrorTypeAuth = "Auth"
	// ErrorTypeTransient identifies a retryable provider or storage failure.
	ErrorTypeTransient = "Transient"
	// ErrorTypeSessionLost identifies a tool worker Session that cannot continue.
	ErrorTypeSessionLost = "SessionLost"
	// ErrorTypeAmbiguousToolExecution identifies a tool that may have run before interruption.
	ErrorTypeAmbiguousToolExecution = "AmbiguousToolExecution"
)
