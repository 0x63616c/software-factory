// Package e2e owns the deterministic standalone acceptance harness.
//
// The executable test files require the e2e build tag because the harness
// starts disposable Postgres and Temporal services. Run it through `just e2e`.
package e2e
