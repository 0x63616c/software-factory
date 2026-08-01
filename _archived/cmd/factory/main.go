// Command factory is the software factory: a TUI that takes tickets and produces
// code merged to production, built to operate on any codebase — including itself.
//
// This file is the COMPOSITION ROOT — the single place where the whole dependency
// graph is wired by hand (SoftwareStyle uses manual constructor injection; there is
// no runtime DI container by design). The startup sequence, once the runtime spine
// exists, is:
//
//	parse flags/env/file  ->  build Config  ->  Config.Validate()  ->  wire the graph  ->  launch the TUI
//
// Nothing is wired yet: the standards come first. See AGENTS.md and docs/SoftwareStyle.md.
package main

func main() {
	// TODO(runtime-spine): config load + Validate(), supervised-worker supervisor,
	// store (sqlite/goose/sqlc), the headless engine, and the bubbletea TUI wired
	// via the EventSink seam. Tracked by the ADRs in docs/adr/.
}
