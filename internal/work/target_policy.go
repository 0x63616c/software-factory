package work

import (
	"fmt"
	"strings"
	"time"
)

// TargetRunPolicy is the immutable resolved policy carried by one target Run.
//
// Target workflows receive this value in their input and never consult
// deployment configuration again.
type TargetRunPolicy struct {
	// SemanticDeadline stops new work before the hard workflow deadline so a
	// terminal result can still be recorded durably.
	SemanticDeadline time.Duration

	// HardDeadline is the workflow's final ceiling, including finalization.
	HardDeadline time.Duration

	// RequiredChecks is the explicit GitHub check-name set that may authorize
	// this Run's candidate commit. It must never be inferred from a snapshot.
	RequiredChecks []string

	Agent              AgentActivityPolicy
	AwaitCI            ActivityPolicy
	Merge              ActivityPolicy
	Provisioning       ActivityPolicy
	CredentialRotation ActivityPolicy
	Recording          ActivityPolicy
	Teardown           ActivityPolicy
}

// ActivityPolicy holds the retry and timeout settings for one named technical
// activity domain. A zero MaximumAttempts means Temporal retries until the
// ScheduleToClose timeout.
type ActivityPolicy struct {
	StartToCloseTimeout    time.Duration
	ScheduleToCloseTimeout time.Duration
	Retry                  RetryPolicy
}

// Validate reports whether a named technical activity policy is usable.
func (p ActivityPolicy) Validate(name string) error {
	if p.StartToCloseTimeout <= 0 {
		return fmt.Errorf("%w: %s start-to-close timeout must be positive", ErrInvalidRun, name)
	}
	if p.ScheduleToCloseTimeout <= 0 {
		return fmt.Errorf("%w: %s schedule-to-close timeout must be positive", ErrInvalidRun, name)
	}
	if p.ScheduleToCloseTimeout < p.StartToCloseTimeout {
		return fmt.Errorf("%w: %s schedule-to-close timeout %s is shorter than start-to-close timeout %s",
			ErrInvalidRun, name, p.ScheduleToCloseTimeout, p.StartToCloseTimeout)
	}
	if err := p.Retry.Validate(name); err != nil {
		return err
	}
	return nil
}

// AgentActivityPolicy is the activity policy for one already-authorized Agent
// Attempt. Its retry policy resumes that same attempt; it never creates another
// semantic attempt.
type AgentActivityPolicy struct {
	StartToCloseTimeout    time.Duration
	ScheduleToCloseTimeout time.Duration
	HeartbeatTimeout       time.Duration
	Retry                  RetryPolicy
}

// Validate reports whether p defines a usable retry and timeout shape for one
// direct model turn.
func (p AgentActivityPolicy) Validate() error {
	if p.StartToCloseTimeout <= 0 {
		return fmt.Errorf("%w: agent start-to-close timeout must be positive", ErrInvalidRun)
	}
	if p.ScheduleToCloseTimeout <= 0 {
		return fmt.Errorf("%w: agent schedule-to-close timeout must be positive", ErrInvalidRun)
	}
	if p.HeartbeatTimeout <= 0 {
		return fmt.Errorf("%w: agent heartbeat timeout must be positive", ErrInvalidRun)
	}
	if p.HeartbeatTimeout >= p.StartToCloseTimeout {
		return fmt.Errorf("%w: agent heartbeat timeout must be shorter than its start-to-close timeout", ErrInvalidRun)
	}
	if p.ScheduleToCloseTimeout < p.StartToCloseTimeout {
		return fmt.Errorf("%w: agent schedule-to-close timeout is shorter than start-to-close timeout", ErrInvalidRun)
	}
	return p.Retry.Validate("agent")
}

// RetryPolicy is an SDK-independent resolved Temporal retry policy.
type RetryPolicy struct {
	InitialInterval    time.Duration
	BackoffCoefficient float64
	MaximumInterval    time.Duration
	MaximumAttempts    int32
}

// Validate reports whether this retry policy asks Temporal for a defined retry
// shape. MaximumAttempts may be zero to deliberately permit retries until the
// enclosing schedule-to-close deadline.
func (p RetryPolicy) Validate(name string) error {
	if p.InitialInterval <= 0 {
		return fmt.Errorf("%w: %s retry initial interval must be positive", ErrInvalidRun, name)
	}
	if p.BackoffCoefficient < 1 {
		return fmt.Errorf("%w: %s retry backoff coefficient must be at least one", ErrInvalidRun, name)
	}
	if p.MaximumInterval < p.InitialInterval {
		return fmt.Errorf("%w: %s retry maximum interval %s is shorter than initial interval %s",
			ErrInvalidRun, name, p.MaximumInterval, p.InitialInterval)
	}
	if p.MaximumAttempts < 0 {
		return fmt.Errorf("%w: %s retry attempts cannot be negative", ErrInvalidRun, name)
	}
	return nil
}

// DefaultTargetRunPolicy returns the resolved policy for target Runs. Its
// literals live here so a Run's input records one complete, replay-stable
// decision.
func DefaultTargetRunPolicy() TargetRunPolicy {
	return TargetRunPolicy{
		SemanticDeadline: 23 * time.Hour,
		HardDeadline:     24 * time.Hour,
		RequiredChecks:   []string{"test-software-factory"},
		Agent: AgentActivityPolicy{
			StartToCloseTimeout:    55 * time.Minute,
			ScheduleToCloseTimeout: 90 * time.Minute,
			HeartbeatTimeout:       5 * time.Minute,
			Retry: RetryPolicy{
				InitialInterval:    10 * time.Second,
				BackoffCoefficient: 2,
				MaximumInterval:    5 * time.Minute,
				MaximumAttempts:    10,
			},
		},
		AwaitCI:            boundedActivityPolicy(time.Minute, 2*time.Hour, 15*time.Second, 2, 5*time.Minute, 0),
		Merge:              boundedActivityPolicy(time.Minute, 30*time.Minute, 10*time.Second, 2, 5*time.Minute, 0),
		Provisioning:       boundedActivityPolicy(2*time.Minute, 30*time.Minute, 10*time.Second, 2, 5*time.Minute, 10),
		CredentialRotation: boundedActivityPolicy(time.Minute, 15*time.Minute, 10*time.Second, 2, 5*time.Minute, 10),
		Recording:          boundedActivityPolicy(time.Minute, time.Hour, 10*time.Second, 2, 5*time.Minute, 0),
		Teardown:           boundedActivityPolicy(2*time.Minute, 30*time.Minute, 10*time.Second, 2, 5*time.Minute, 10),
	}
}

func boundedActivityPolicy(startToClose, scheduleToClose, initial time.Duration, coefficient float64, maximum time.Duration, attempts int32) ActivityPolicy {
	return ActivityPolicy{
		StartToCloseTimeout:    startToClose,
		ScheduleToCloseTimeout: scheduleToClose,
		Retry: RetryPolicy{
			InitialInterval: initial, BackoffCoefficient: coefficient, MaximumInterval: maximum, MaximumAttempts: attempts,
		},
	}
}

// Validate reports why a target Run policy cannot safely start.
func (p TargetRunPolicy) Validate() error {
	if p.SemanticDeadline <= 0 {
		return fmt.Errorf("%w: semantic deadline must be positive", ErrInvalidRun)
	}
	if p.HardDeadline <= p.SemanticDeadline {
		return fmt.Errorf("%w: hard deadline %s must leave finalization time after semantic deadline %s",
			ErrInvalidRun, p.HardDeadline, p.SemanticDeadline)
	}
	if len(p.RequiredChecks) == 0 {
		return fmt.Errorf("%w: target runs require a non-empty explicit required-check set", ErrInvalidRun)
	}
	checks := make(map[string]struct{}, len(p.RequiredChecks))
	for _, check := range p.RequiredChecks {
		if strings.TrimSpace(check) == "" {
			return fmt.Errorf("%w: required check names cannot be empty", ErrInvalidRun)
		}
		if _, exists := checks[check]; exists {
			return fmt.Errorf("%w: required check %q appears more than once", ErrInvalidRun, check)
		}
		checks[check] = struct{}{}
	}
	if err := p.validateAgent(); err != nil {
		return err
	}
	for _, named := range []struct {
		name   string
		policy ActivityPolicy
	}{
		{name: "await CI", policy: p.AwaitCI},
		{name: "merge", policy: p.Merge},
		{name: "provisioning", policy: p.Provisioning},
		{name: "credential rotation", policy: p.CredentialRotation},
		{name: "recording", policy: p.Recording},
		{name: "teardown", policy: p.Teardown},
	} {
		if err := named.policy.Validate(named.name); err != nil {
			return err
		}
	}
	return nil
}

func (p TargetRunPolicy) validateAgent() error {
	return p.Agent.Validate()
}
