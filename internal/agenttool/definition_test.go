package agenttool_test

import (
	"encoding/json"
	"testing"

	"github.com/0x63616c/software-factory/internal/agenttool"
)

type readInput struct {
	Path  string `json:"path" jsonschema_description:"Repository-relative file path to read."`
	Limit int    `json:"limit" jsonschema:"minimum=1,maximum=1024" jsonschema_description:"Maximum bytes to return."`
}

func TestDefineProducesStrictSchemaFromHandlerInput(t *testing.T) {
	t.Parallel()

	definition := agenttool.Define[readInput](
		"read_file",
		"Read a bounded region of a repository file.",
	)

	specification := definition.Specification()
	if specification.Name != "read_file" {
		t.Fatalf("Name = %q, want read_file", specification.Name)
	}
	if specification.Description != "Read a bounded region of a repository file." {
		t.Fatalf("Description = %q", specification.Description)
	}

	want := []byte(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Repository-relative file path to read."
			},
			"limit": {
				"type": "integer",
				"minimum": 1,
				"maximum": 1024,
				"description": "Maximum bytes to return."
			}
		},
		"required": ["path", "limit"],
		"additionalProperties": false
	}`)
	assertJSONEqual(t, specification.Parameters, want)
}

func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()

	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("generated schema is not JSON: %v\nschema: %s", err, got)
	}
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("test schema is not JSON: %v", err)
	}

	gotJSON, err := json.Marshal(gotValue)
	if err != nil {
		t.Fatalf("normalizing generated schema: %v", err)
	}
	wantJSON, err := json.Marshal(wantValue)
	if err != nil {
		t.Fatalf("normalizing expected schema: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("schema = %s, want %s", gotJSON, wantJSON)
	}
}
