package work

// TaskQueue is the Temporal task queue this service's workflows and activities
// are scheduled on, and the only place its name is written.
//
// It is here because the name is PUBLISHED. An operator types it into
// `temporal task-queue describe`, the first-run runbook names it, and the
// composition root needs it in Go to register a worker that polls it. A name
// that leaves the process has one home; that is the whole reason, and it does
// not need a second consumer inside the codebase to earn it.
//
// Workflow code normally inherits its parent's queue. The target Dispatcher is
// deliberately different: it runs on TargetDispatcherTaskQueue, while its
// WorkOnTicket children must run on this queue. The child-options builder names
// this constant rather than repeating its string.
//
// It is a constant rather than configuration for a related reason. A queue name
// settable per environment would let the worker and whatever schedules onto it
// disagree at runtime, and that failure is silent at both ends — a worker
// polling a queue nobody sends to looks exactly like a system with nothing to
// do. Pointing a worker at an experimental queue is a change to this line and a
// deploy: deliberate, reviewable, and impossible to do to one side only.
//
// One string, two concepts: the Temporal NAMESPACE is also "software-factory"
// (infra/src/temporal.ts). They are passed on the same command lines, so a
// transposed --namespace and --task-queue is invisible.
//
// The same fact by the same rule as WorkflowID in paths.go: one name, one home,
// nothing else may construct it.
const TaskQueue = "software-factory"

// TargetDispatcherTaskQueue is the inactive control-only queue that serves
// policy publication before the target main worker begins polling.
const TargetDispatcherTaskQueue = "software-factory-dispatcher-control"
