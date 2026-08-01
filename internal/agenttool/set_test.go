package agenttool_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/agenttool"
)

type execInput struct {
	Argv []string `json:"argv" jsonschema:"minItems=1" jsonschema_description:"Command and arguments to execute."`
}

type undocumentedInput struct {
	Path string `json:"path"`
}

type scalarInput string

type arbitraryMapInput struct {
	Values map[string]string `json:"values" jsonschema_description:"Values indexed by arbitrary keys."`
}

func TestMustSetSortsSpecifications(t *testing.T) {
	t.Parallel()

	read := agenttool.Bind(
		agenttool.Define[readInput]("read_file", "Read a repository file."),
		func(_ context.Context, _ readInput) (agenttool.Result, error) { return agenttool.Result{}, nil },
	)
	exec := agenttool.Bind(
		agenttool.Define[execInput]("exec_command", "Execute one argv command."),
		func(_ context.Context, _ execInput) (agenttool.Result, error) { return agenttool.Result{}, nil },
	)

	set := agenttool.MustSet("coding-write-v1", read, exec)
	specifications := set.Specifications()
	if len(specifications) != 2 {
		t.Fatalf("len(Specifications()) = %d, want 2", len(specifications))
	}
	if specifications[0].Name != "exec_command" || specifications[1].Name != "read_file" {
		t.Fatalf("Specifications() names = [%q, %q]", specifications[0].Name, specifications[1].Name)
	}
}

func TestMustSetRejectsDuplicateTools(t *testing.T) {
	t.Parallel()

	first := agenttool.Bind(
		agenttool.Define[readInput]("read_file", "Read a repository file."),
		func(_ context.Context, _ readInput) (agenttool.Result, error) { return agenttool.Result{}, nil },
	)
	second := agenttool.Bind(
		agenttool.Define[semanticInput]("read_file", "Read a repository file another way."),
		func(_ context.Context, _ semanticInput) (agenttool.Result, error) { return agenttool.Result{}, nil },
	)

	defer func() {
		panicValue := recover()
		if panicValue == nil {
			t.Fatal("MustSet() did not panic")
		}
		if message := fmt.Sprint(panicValue); !strings.Contains(message, `duplicate tool "read_file"`) {
			t.Fatalf("panic = %q", message)
		}
	}()
	agenttool.MustSet("coding-read-v1", first, second)
}

func TestMustSetFingerprintIsStableAcrossRegistrationOrder(t *testing.T) {
	t.Parallel()

	read := agenttool.Bind(
		agenttool.Define[readInput]("read_file", "Read a repository file."),
		func(_ context.Context, _ readInput) (agenttool.Result, error) { return agenttool.Result{}, nil },
	)
	exec := agenttool.Bind(
		agenttool.Define[execInput]("exec_command", "Execute one argv command."),
		func(_ context.Context, _ execInput) (agenttool.Result, error) { return agenttool.Result{}, nil },
	)

	forward := agenttool.MustSet("coding-write-v1", read, exec).Fingerprint()
	reverse := agenttool.MustSet("coding-write-v1", exec, read).Fingerprint()
	if forward == "" || forward != reverse {
		t.Fatalf("fingerprints = %q and %q", forward, reverse)
	}
}

func TestMustSetRejectsBlankToolsetID(t *testing.T) {
	t.Parallel()

	read := agenttool.Bind(
		agenttool.Define[readInput]("read_file", "Read a repository file."),
		func(_ context.Context, _ readInput) (agenttool.Result, error) { return agenttool.Result{}, nil },
	)

	defer func() {
		panicValue := recover()
		if panicValue == nil {
			t.Fatal("MustSet() did not panic")
		}
		if message := fmt.Sprint(panicValue); !strings.Contains(message, "toolset id is blank") {
			t.Fatalf("panic = %q", message)
		}
	}()
	agenttool.MustSet("", read)
}

func TestMustSetRejectsBlankToolIdentity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		toolName    string
		description string
		wantPanic   string
	}{
		{name: "name", toolName: " ", description: "Read a repository file.", wantPanic: "tool name is blank"},
		{name: "description", toolName: "read_file", description: " ", wantPanic: `tool "read_file" description is blank`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tool := agenttool.Bind(
				agenttool.Define[readInput](testCase.toolName, testCase.description),
				func(_ context.Context, _ readInput) (agenttool.Result, error) { return agenttool.Result{}, nil },
			)
			defer func() {
				panicValue := recover()
				if panicValue == nil {
					t.Fatal("MustSet() did not panic")
				}
				if message := fmt.Sprint(panicValue); !strings.Contains(message, testCase.wantPanic) {
					t.Fatalf("panic = %q", message)
				}
			}()
			agenttool.MustSet("coding-read-v1", tool)
		})
	}
}

func TestMustSetRejectsNilTools(t *testing.T) {
	t.Parallel()

	t.Run("nil interface", func(t *testing.T) {
		defer func() {
			panicValue := recover()
			if message := fmt.Sprint(panicValue); !strings.Contains(message, "nil tool") {
				t.Fatalf("panic = %q", message)
			}
		}()
		agenttool.MustSet("coding-read-v1", nil)
	})

	t.Run("typed nil", func(t *testing.T) {
		var tool *agenttool.BoundTool[readInput]
		defer func() {
			panicValue := recover()
			if message := fmt.Sprint(panicValue); !strings.Contains(message, "nil tool") {
				t.Fatalf("panic = %q", message)
			}
		}()
		agenttool.MustSet("coding-read-v1", tool)
	})
}

func TestMustSetRejectsMissingPropertyDescriptions(t *testing.T) {
	t.Parallel()

	tool := agenttool.Bind(
		agenttool.Define[undocumentedInput]("read_file", "Read a repository file."),
		func(_ context.Context, _ undocumentedInput) (agenttool.Result, error) { return agenttool.Result{}, nil },
	)
	defer func() {
		panicValue := recover()
		if message := fmt.Sprint(panicValue); !strings.Contains(message, `property "path" description is blank`) {
			t.Fatalf("panic = %q", message)
		}
	}()
	agenttool.MustSet("coding-read-v1", tool)
}

func TestMustSetRejectsNonObjectInputs(t *testing.T) {
	t.Parallel()

	tool := agenttool.Bind(
		agenttool.Define[scalarInput]("echo", "Echo one string."),
		func(_ context.Context, _ scalarInput) (agenttool.Result, error) { return agenttool.Result{}, nil },
	)
	defer func() {
		panicValue := recover()
		if message := fmt.Sprint(panicValue); !strings.Contains(message, `root type is "string", want object`) {
			t.Fatalf("panic = %q", message)
		}
	}()
	agenttool.MustSet("coding-read-v1", tool)
}

func TestMustSetRejectsUnsupportedStrictSchemas(t *testing.T) {
	t.Parallel()

	tool := agenttool.Bind(
		agenttool.Define[arbitraryMapInput]("lookup", "Look up arbitrary values."),
		func(_ context.Context, _ arbitraryMapInput) (agenttool.Result, error) { return agenttool.Result{}, nil },
	)
	defer func() {
		panicValue := recover()
		if message := fmt.Sprint(panicValue); !strings.Contains(message, `property "values" permits arbitrary object keys`) {
			t.Fatalf("panic = %q", message)
		}
	}()
	agenttool.MustSet("coding-read-v1", tool)
}

func TestSetExecutesToolByName(t *testing.T) {
	t.Parallel()

	var received readInput
	read := agenttool.Bind(
		agenttool.Define[readInput]("read_file", "Read a repository file."),
		func(_ context.Context, input readInput) (agenttool.Result, error) {
			received = input
			return agenttool.Result{Content: "contents"}, nil
		},
	)
	set := agenttool.MustSet("coding-read-v1", read)

	result, err := set.Execute(context.Background(), "read_file", []byte(`{"path":"README.md","limit":128}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if received.Path != "README.md" {
		t.Fatalf("handler input path = %q", received.Path)
	}
	if result.Content != "contents" || result.IsError {
		t.Fatalf("Execute() result = %+v", result)
	}
}

func TestSetReturnsToolErrorForUnknownName(t *testing.T) {
	t.Parallel()

	set := agenttool.MustSet("coding-read-v1")
	result, err := set.Execute(context.Background(), "delete_everything", []byte(`{}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError || result.Content != `unknown tool "delete_everything"` {
		t.Fatalf("Execute() result = %+v", result)
	}
}
