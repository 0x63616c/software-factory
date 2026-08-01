package work

// RunState is the target workflow execution state needed by maintenance.
type RunState struct {
	Open  bool
	RunID string
}
