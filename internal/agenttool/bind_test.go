package agenttool_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/agenttool"
)

type semanticInput struct {
	Path string `json:"path" jsonschema_description:"Repository-relative file path to read."`
}

func (input semanticInput) Validate() error {
	if input.Path == "." {
		return errors.New("path must name a file")
	}
	return nil
}

func TestBindDecodesAndExecutesTheTypedInput(t *testing.T) {
	t.Parallel()

	definition := agenttool.Define[readInput](
		"read_file",
		"Read a bounded region of a repository file.",
	)
	tool := agenttool.Bind(definition, func(_ context.Context, input readInput) (agenttool.Result, error) {
		if input.Path != "docs/design.md" || input.Limit != 512 {
			t.Fatalf("input = %#v", input)
		}
		return agenttool.Result{Content: "design"}, nil
	})

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"docs/design.md","limit":512}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Content != "design" || result.IsError {
		t.Fatalf("result = %#v", result)
	}
}

func TestBindReturnsToolErrorForUnknownOrTrailingFields(t *testing.T) {
	t.Parallel()

	definition := agenttool.Define[readInput](
		"read_file",
		"Read a bounded region of a repository file.",
	)
	tool := agenttool.Bind(definition, func(_ context.Context, _ readInput) (agenttool.Result, error) {
		t.Fatal("handler executed for invalid arguments")
		return agenttool.Result{}, nil
	})

	arguments := []json.RawMessage{
		json.RawMessage(`{"path":"docs/design.md","limit":512,"surprise":true}`),
		json.RawMessage(`{"path":"docs/design.md","limit":512} {}`),
	}
	for _, argument := range arguments {
		result, err := tool.Execute(context.Background(), argument)
		if err != nil {
			t.Fatalf("Execute(%s) error = %v", argument, err)
		}
		if !result.IsError || result.Content == "" {
			t.Fatalf("Execute(%s) result = %#v", argument, result)
		}
	}
}

func TestBindReturnsToolErrorForSchemaConstraint(t *testing.T) {
	t.Parallel()

	definition := agenttool.Define[readInput](
		"read_file",
		"Read a bounded region of a repository file.",
	)
	tool := agenttool.Bind(definition, func(_ context.Context, _ readInput) (agenttool.Result, error) {
		t.Fatal("handler executed for schema-invalid arguments")
		return agenttool.Result{}, nil
	})

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"docs/design.md","limit":0}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError || result.Content == "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestBindRunsSemanticValidationBeforeTheHandler(t *testing.T) {
	t.Parallel()

	definition := agenttool.Define[semanticInput](
		"read_file",
		"Read a bounded region of a repository file.",
	)
	tool := agenttool.Bind(definition, func(_ context.Context, _ semanticInput) (agenttool.Result, error) {
		t.Fatal("handler executed for semantically invalid arguments")
		return agenttool.Result{}, nil
	})

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"."}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "path must name a file") {
		t.Fatalf("result = %#v", result)
	}
}
