# ADR-0011: Headless engine, thin TUI, EventSink seam

- Status: Accepted
- Date: 2026-07-23

## Context
bubbletea is the Elm architecture (`Model`/`Update`/`View`/`Msg`/`Cmd`). The risk is
business logic leaking into `Update` methods, which would make the factory untestable
without a terminal and scatter "where's the logic?" across the UI. Separately, the
engine is concurrent (supervised workers) while the TUI loop is single-threaded — they
must meet somewhere. And it's undecided whether the runner should eventually be a
separate daemon.

## Decision
- **The engine is headless** — it knows nothing about bubbletea. It is a complete Go
  program tested with no terminal.
- **The TUI is a thin presentation layer**: it only (a) translates input → engine
  commands (as `tea.Cmd`) and (b) translates engine events → view state. Zero domain
  logic in `Update`/`View`.
- **`EventSink` interface is the engine→TUI seam.** The engine publishes domain events
  to an abstract sink; in-process it's backed by a Go channel that one `tea.Cmd` reads
  and converts to `Msg`. The engine never imports bubbletea — **depguard forbids it.**
- **Styling is centralized** in a `theme` package (lipgloss), never inline.
- **The daemon question is explicitly DEFERRED behind the EventSink seam.** The day we
  want a separate runner, the sink's implementation swaps from a channel to an IPC
  stream; engine and TUI logic don't change.

## Rejected alternatives
- **Business logic in bubbletea `Update`**: rejected — the TUI equivalent of `os.Getenv`
  in a random file; kills testability and legibility.
- **Committing to a daemon now**: rejected — the EventSink abstraction absorbs the
  decision, so we get the option for free without paying today.
- **A raw channel as the contract** (vs an interface): the channel is just one *impl* of
  the real invariant (engine emits to an abstract sink).

## Consequences
~95% of the system tests as plain Go; a headless run mode falls out nearly free.
`teatest` tests TUI logic; `tu` smoke-tests the real terminal (ADR-0012).
