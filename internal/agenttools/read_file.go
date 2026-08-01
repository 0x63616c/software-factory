// Package agenttools implements sandbox-confined tools for AgentWorkflow.
package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/agenttool"
)

const maxReadFileBytes = 4 << 20

// ReadFileInput is the model-facing input for read_file.
type ReadFileInput struct {
	Path string `json:"path" jsonschema_description:"Repository-relative file path to read."`
}

// ReadFileOutput is the bounded model-facing read result.
type ReadFileOutput struct {
	Path      string          `json:"path"`
	Preview   string          `json:"preview"`
	Truncated bool            `json:"truncated"`
	OutputRef agent.OutputRef `json:"output_ref,omitempty"`
}

// NewReadFile builds a repository-confined read_file tool.
func NewReadFile(
	repositoryRoot string,
	artifactIdentity string,
	artifacts agent.ArtifactStore,
	maxInlineBytes int,
) (*agenttool.BoundTool[ReadFileInput], error) {
	if strings.TrimSpace(repositoryRoot) == "" || !filepath.IsAbs(repositoryRoot) {
		return nil, fmt.Errorf("read_file repository root must be absolute")
	}
	if strings.TrimSpace(artifactIdentity) == "" {
		return nil, fmt.Errorf("read_file needs an artifact identity")
	}
	if maxInlineBytes < 1 {
		return nil, fmt.Errorf("read_file max inline bytes must be positive")
	}
	definition := agenttool.Define[ReadFileInput]("read_file", "Read one file inside the ticket repository.")
	return agenttool.Bind(definition, func(ctx context.Context, input ReadFileInput) (agenttool.Result, error) {
		root, err := resolveRepositoryRoot(repositoryRoot)
		if err != nil {
			return toolError("repository is unavailable: %v", err), nil
		}
		target, result := confinedFile(root, input.Path)
		if result.IsError {
			return result, nil
		}
		info, err := os.Stat(target)
		if err != nil {
			return toolError("cannot stat %q: %v", input.Path, err), nil
		}
		if !info.Mode().IsRegular() {
			return toolError("%q is not a regular file", input.Path), nil
		}
		if info.Size() > maxReadFileBytes {
			return toolError("%q is too large to read", input.Path), nil
		}
		content, err := os.ReadFile(target)
		if err != nil {
			return toolError("cannot read %q: %v", input.Path, err), nil
		}
		output := ReadFileOutput{Path: input.Path, Preview: string(content)}
		if len(content) > maxInlineBytes {
			ref, err := artifacts.StoreOutput(ctx, artifactIdentity, content)
			if err != nil {
				return agenttool.Result{}, fmt.Errorf("store oversized read_file output: %w", err)
			}
			output.Preview = string(content[:maxInlineBytes])
			output.Truncated = true
			output.OutputRef = ref
		}
		encoded, err := json.Marshal(output)
		if err != nil {
			return agenttool.Result{}, fmt.Errorf("encode read_file output: %w", err)
		}
		return agenttool.Result{Content: string(encoded)}, nil
	}), nil
}

func resolveRepositoryRoot(root string) (string, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root symlinks: %w", err)
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("make repository root absolute: %w", err)
	}
	return absolute, nil
}

func confinedFile(root, requested string) (string, agenttool.Result) {
	if requested == "" || filepath.IsAbs(requested) {
		return "", toolError("path %q is outside repository", requested)
	}
	joined := filepath.Join(root, filepath.Clean(requested))
	if !inside(root, joined) {
		return "", toolError("path %q is outside repository", requested)
	}
	target, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", toolError("cannot resolve %q: %v", requested, err)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return "", toolError("cannot resolve %q: %v", requested, err)
	}
	if !inside(root, target) {
		return "", toolError("path %q is outside repository", requested)
	}
	return target, agenttool.Result{}
}

func inside(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func toolError(format string, args ...any) agenttool.Result {
	return agenttool.Result{Content: fmt.Sprintf(format, args...), IsError: true}
}
