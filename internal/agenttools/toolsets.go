package agenttools

import (
	"fmt"
	"time"

	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/agenttool"
	"github.com/0x63616c/software-factory/internal/blobs"
)

const maxInlineToolOutputBytes = 64 << 10

// NewToolsets constructs both immutable production catalogues from the same
// Go definitions used by the sandbox handlers. Repository resolution is
// deferred until execution so the main worker can advertise schemas without a
// checkout and the sandbox worker can start before CloneRepo completes.
func NewToolsets(repositoryRoot, artifactIdentity string, blobStore blobs.Store) ([]agenttool.Set, error) {
	if blobStore == nil {
		return nil, fmt.Errorf("agent toolsets need a blob store")
	}
	artifacts := agent.NewArtifactStore(blobStore)
	readFile, err := NewReadFile(repositoryRoot, artifactIdentity, artifacts, maxInlineToolOutputBytes)
	if err != nil {
		return nil, fmt.Errorf("build read_file tool: %w", err)
	}
	readOnlyExec, err := NewReadOnlyExecCommand(
		repositoryRoot, artifactIdentity, artifacts, maxInlineToolOutputBytes, agent.MaxToolExecutionDuration,
	)
	if err != nil {
		return nil, fmt.Errorf("build read-only exec_command tool: %w", err)
	}
	writeExec, err := NewExecCommand(
		repositoryRoot, artifactIdentity, artifacts, maxInlineToolOutputBytes, agent.MaxToolExecutionDuration,
	)
	if err != nil {
		return nil, fmt.Errorf("build write exec_command tool: %w", err)
	}
	applyPatch, err := NewApplyPatch(repositoryRoot, time.Minute)
	if err != nil {
		return nil, fmt.Errorf("build apply_patch tool: %w", err)
	}
	return []agenttool.Set{
		agenttool.MustSet(agent.ToolsetCodingReadV1, readFile, readOnlyExec),
		agenttool.MustSet(agent.ToolsetCodingWriteV1, readFile, writeExec, applyPatch),
	}, nil
}
