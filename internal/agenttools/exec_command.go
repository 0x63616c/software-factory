package agenttools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/agenttool"
)

const maxCapturedStreamBytes = 4 << 20

// ExecCommandInput is the model-facing structured process request.
type ExecCommandInput struct {
	Argv []string `json:"argv" jsonschema:"minItems=1" jsonschema_description:"Executable followed by its argument vector; no implicit shell is used."`
}

// ExecCommandOutput is the bounded result of one local process.
type ExecCommandOutput struct {
	ExitCode      int             `json:"exit_code"`
	StdoutPreview string          `json:"stdout_preview"`
	StderrPreview string          `json:"stderr_preview"`
	StdoutRef     agent.OutputRef `json:"stdout_ref,omitempty"`
	StderrRef     agent.OutputRef `json:"stderr_ref,omitempty"`
	StdoutDropped bool            `json:"stdout_dropped"`
	StderrDropped bool            `json:"stderr_dropped"`
}

// NewExecCommand builds an argv-only command tool rooted in the repository.
func NewExecCommand(
	repositoryRoot string,
	artifactIdentity string,
	artifacts agent.ArtifactStore,
	maxInlineBytes int,
	timeout time.Duration,
) (*agenttool.BoundTool[ExecCommandInput], error) {
	return newExecCommand(repositoryRoot, artifactIdentity, artifacts, maxInlineBytes, timeout, nil)
}

// NewReadOnlyExecCommand builds the capability-restricted exec_command used by plan and review.
func NewReadOnlyExecCommand(
	repositoryRoot string,
	artifactIdentity string,
	artifacts agent.ArtifactStore,
	maxInlineBytes int,
	timeout time.Duration,
) (*agenttool.BoundTool[ExecCommandInput], error) {
	return newExecCommand(repositoryRoot, artifactIdentity, artifacts, maxInlineBytes, timeout, readOnlyCommand)
}

func newExecCommand(
	repositoryRoot string,
	artifactIdentity string,
	artifacts agent.ArtifactStore,
	maxInlineBytes int,
	timeout time.Duration,
	policy func([]string) error,
) (*agenttool.BoundTool[ExecCommandInput], error) {
	if !filepath.IsAbs(repositoryRoot) || artifactIdentity == "" || maxInlineBytes < 1 || timeout <= 0 {
		return nil, fmt.Errorf("exec_command needs artifact identity, positive preview size, and positive timeout")
	}
	definition := agenttool.Define[ExecCommandInput]("exec_command", "Execute one explicit argv command in the ticket repository.")
	return agenttool.Bind(definition, func(ctx context.Context, input ExecCommandInput) (agenttool.Result, error) {
		root, err := resolveRepositoryRoot(repositoryRoot)
		if err != nil {
			return toolError("repository is unavailable: %v", err), nil
		}
		if policy != nil {
			if err := policy(input.Argv); err != nil {
				return toolError("read-only exec_command rejected argv: %v", err), nil
			}
		}
		return executeCommand(ctx, root, artifactIdentity, artifacts, maxInlineBytes, timeout, input)
	}), nil
}

func readOnlyCommand(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("command is required")
	}
	if filepath.Base(argv[0]) != argv[0] {
		return fmt.Errorf("command %q must be an allowlisted bare executable name", argv[0])
	}
	command := filepath.Base(argv[0])
	switch command {
	case "git":
		return readOnlyGitCommand(argv[1:])
	case "rg":
		return readOnlyRipgrepCommand(argv[1:])
	default:
		return fmt.Errorf("command %q is not allowlisted", command)
	}
}

func readOnlyGitCommand(arguments []string) error {
	if len(arguments) == 0 {
		return fmt.Errorf("git subcommand is required")
	}
	allowedOptions, ok := gitReadOnlyOptions[arguments[0]]
	if !ok {
		return fmt.Errorf("git subcommand %q is mutating or unsupported", arguments[0])
	}
	return validateReadOnlyArguments(arguments[1:], allowedOptions)
}

func readOnlyRipgrepCommand(arguments []string) error {
	return validateReadOnlyArguments(arguments, ripgrepReadOnlyOptions)
}

type optionGrammar struct {
	exact       map[string]bool
	valuePrefix []string
}

var commonGitReadOnlyOptions = optionGrammar{
	exact: map[string]bool{
		"--": true, "--no-color": true, "--no-ext-diff": true, "--no-textconv": true,
		"--abbrev-commit": true, "--full-history": true, "--decorate": true, "--no-decorate": true,
	},
	valuePrefix: []string{"--color=", "--format=", "--pretty=", "--date=", "--abbrev="},
}

var gitReadOnlyOptions = map[string]optionGrammar{
	"status": extendOptionGrammar(commonGitReadOnlyOptions,
		[]string{"-s", "-b", "--short", "--branch", "--show-stash", "--ahead-behind", "--no-ahead-behind", "--porcelain"},
		[]string{"--porcelain=", "--untracked-files=", "--ignored=", "--find-renames="}),
	"diff": extendOptionGrammar(commonGitReadOnlyOptions,
		[]string{"-p", "-u", "-U", "--patch", "--no-patch", "--raw", "--stat", "--numstat", "--shortstat", "--summary", "--name-only", "--name-status", "--check", "--cached", "--staged", "--merge-base", "--exit-code", "--quiet", "--word-diff", "--binary"},
		[]string{"-U", "--unified=", "--stat=", "--word-diff=", "--diff-filter=", "--find-renames=", "--find-copies="}),
	"show": extendOptionGrammar(commonGitReadOnlyOptions,
		[]string{"-p", "--patch", "--no-patch", "--raw", "--stat", "--numstat", "--shortstat", "--summary", "--name-only", "--name-status", "--oneline"},
		[]string{"--unified=", "--diff-filter="}),
	"log": extendOptionGrammar(commonGitReadOnlyOptions,
		[]string{"-p", "--patch", "--no-patch", "--stat", "--shortstat", "--name-only", "--name-status", "--oneline", "--graph", "--all", "--first-parent", "--reverse"},
		[]string{"-n", "--max-count=", "--since=", "--until=", "--author=", "--grep="}),
	"grep": extendOptionGrammar(commonGitReadOnlyOptions,
		[]string{"-n", "-i", "-w", "-v", "-F", "-E", "--line-number", "--ignore-case", "--word-regexp", "--invert-match", "--fixed-strings", "--extended-regexp", "--count", "--files-with-matches", "--files-without-match"},
		[]string{"-e", "--regexp="}),
	"rev-parse": extendOptionGrammar(commonGitReadOnlyOptions,
		[]string{"--verify", "--quiet", "--short", "--show-toplevel", "--show-prefix", "--show-cdup", "--is-inside-work-tree", "--is-bare-repository", "--git-common-dir"},
		[]string{"--short="}),
	"ls-files": extendOptionGrammar(commonGitReadOnlyOptions,
		[]string{"-c", "-d", "-m", "-o", "-i", "-s", "-u", "-k", "--cached", "--deleted", "--modified", "--others", "--ignored", "--stage", "--unmerged", "--killed", "--directory", "--empty-directory", "--error-unmatch"},
		[]string{"--exclude=", "--exclude-from=", "--exclude-per-directory="}),
}

var ripgrepReadOnlyOptions = optionGrammar{
	exact: map[string]bool{
		"--": true, "-i": true, "-s": true, "-S": true, "-F": true, "-w": true, "-x": true, "-v": true,
		"-e": true, "-g": true, "-t": true, "-T": true, "-A": true, "-B": true, "-C": true, "-m": true,
		"-n": true, "-N": true, "--hidden": true, "--no-hidden": true, "--ignore-case": true,
		"--case-sensitive": true, "--smart-case": true, "--fixed-strings": true, "--word-regexp": true,
		"--line-regexp": true, "--invert-match": true, "--files": true, "--files-with-matches": true,
		"--files-without-match": true, "--count": true, "--count-matches": true, "--line-number": true,
		"--no-line-number": true, "--column": true, "--heading": true, "--no-heading": true, "--json": true,
		"--stats": true, "--no-messages": true, "--multiline": true, "--multiline-dotall": true, "--crlf": true,
		"--text": true, "--binary": true, "--no-ignore": true, "--no-ignore-vcs": true, "--no-ignore-parent": true,
		"--no-ignore-global": true, "--no-require-git": true, "--one-file-system": true,
	},
	valuePrefix: []string{
		"-e", "--regexp=", "-g", "--glob=", "-t", "--type=", "-T", "--type-not=", "-A", "--after-context=",
		"-B", "--before-context=", "-C", "--context=", "-m", "--max-count=", "--max-depth=", "--max-filesize=",
		"--sort=", "--sortr=", "--threads=", "--color=", "--colors=", "--context-separator=", "--path-separator=",
	},
}

func extendOptionGrammar(base optionGrammar, exact, prefixes []string) optionGrammar {
	combined := optionGrammar{exact: make(map[string]bool, len(base.exact)+len(exact))}
	for option := range base.exact {
		combined.exact[option] = true
	}
	for _, option := range exact {
		combined.exact[option] = true
	}
	combined.valuePrefix = append(append([]string{}, base.valuePrefix...), prefixes...)
	return combined
}

func validateReadOnlyArguments(arguments []string, grammar optionGrammar) error {
	pathsOnly := false
	for _, argument := range arguments {
		if argument == "--" {
			pathsOnly = true
			continue
		}
		if !pathsOnly && strings.HasPrefix(argument, "-") {
			if grammar.exact[argument] || hasAllowedOptionPrefix(argument, grammar.valuePrefix) {
				continue
			}
			return fmt.Errorf("option %q is not in the read-only grammar", argument)
		}
		value := argument
		if _, after, found := strings.Cut(argument, "="); found {
			value = after
		}
		if filepath.IsAbs(value) || value == ".." || strings.HasPrefix(filepath.Clean(value), ".."+string(filepath.Separator)) {
			return fmt.Errorf("path argument %q escapes the repository", argument)
		}
	}
	return nil
}

func hasAllowedOptionPrefix(argument string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(argument, prefix) && len(argument) > len(prefix) {
			return true
		}
	}
	return false
}

func executeCommand(
	ctx context.Context,
	root, artifactIdentity string,
	artifacts agent.ArtifactStore,
	maxInlineBytes int,
	timeout time.Duration,
	input ExecCommandInput,
) (agenttool.Result, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(runCtx, input.Argv[0], input.Argv[1:]...)
	command.Dir = root
	stdout := &cappedCapture{max: maxCapturedStreamBytes}
	stderr := &cappedCapture{max: maxCapturedStreamBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if ctx.Err() != nil {
		return agenttool.Result{}, fmt.Errorf("execute command: %w", ctx.Err())
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return toolError("command exceeded its %s timeout", timeout), nil
	}
	exitCode := 0
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			return toolError("command could not start: %v", err), nil
		}
		exitCode = exitError.ExitCode()
	}
	output := ExecCommandOutput{
		ExitCode:      exitCode,
		StdoutPreview: preview(stdout.Bytes(), maxInlineBytes),
		StderrPreview: preview(stderr.Bytes(), maxInlineBytes),
		StdoutDropped: stdout.dropped,
		StderrDropped: stderr.dropped,
	}
	if len(stdout.Bytes()) > maxInlineBytes {
		ref, err := artifacts.StoreOutput(ctx, artifactIdentity, stdout.Bytes())
		if err != nil {
			return agenttool.Result{}, fmt.Errorf("store exec_command stdout: %w", err)
		}
		output.StdoutRef = ref
	}
	if len(stderr.Bytes()) > maxInlineBytes {
		ref, err := artifacts.StoreOutput(ctx, artifactIdentity, stderr.Bytes())
		if err != nil {
			return agenttool.Result{}, fmt.Errorf("store exec_command stderr: %w", err)
		}
		output.StderrRef = ref
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return agenttool.Result{}, fmt.Errorf("encode exec_command output: %w", err)
	}
	return agenttool.Result{Content: string(encoded), IsError: exitCode != 0}, nil
}

type cappedCapture struct {
	bytes.Buffer
	max     int
	dropped bool
}

func (capture *cappedCapture) Write(value []byte) (int, error) {
	originalLength := len(value)
	remaining := capture.max - capture.Len()
	if remaining <= 0 {
		capture.dropped = true
		return originalLength, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		capture.dropped = true
	}
	_, _ = capture.Buffer.Write(value)
	return originalLength, nil
}

func preview(value []byte, maximum int) string {
	if len(value) > maximum {
		value = value[:maximum]
	}
	return string(value)
}
