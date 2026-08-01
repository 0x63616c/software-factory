package prompts

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
)

// schemaDoc is as much of a stage's JSON Schema as this file compares
// against: the property names it declares, which of them are required, and
// whether it refuses anything else.
type schemaDoc struct {
	AdditionalProperties *bool                `json:"additionalProperties"`
	Required             []string             `json:"required"`
	Properties           map[string]schemaDoc `json:"properties"`
	Items                *schemaDoc           `json:"items"`
}

func readSchema(t *testing.T, file string) schemaDoc {
	t.Helper()

	raw, err := templates.ReadFile(file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	var doc schemaDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}
	return doc
}

// jsonFields is a struct's json tag names, which is what the decoder will
// accept and nothing more: every envelope here decodes with
// DisallowUnknownFields.
func jsonFields(shape any) []string {
	typ := reflect.TypeOf(shape)
	names := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func keys(m map[string]schemaDoc) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TestEveryStagesSchemaAndDecoderDeclareTheSameFields is the check this
// package's own doc comment promises and did not have: "one JSON Schema and
// one Go decoder per stage, so the writer of a stage's schema and the reader
// of its result cannot drift apart."
//
// Nothing enforced that. A field added to the Go envelope but not the schema
// decodes fine and is simply never populated, because the model was never
// told the field exists — silent, and green. A field added to the schema but
// not the envelope is worse: DisallowUnknownFields turns a model that
// obediently answers with it into a failed stage.
func TestEveryStagesSchemaAndDecoderDeclareTheSameFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		stage    work.Stage
		envelope any
	}{
		{work.StagePlan, documentEnvelope{}},
		{work.StageImplement, implementEnvelope{}},
		{work.StageReview, reviewEnvelope{}},
	}

	for _, tc := range cases {
		t.Run(string(tc.stage), func(t *testing.T) {
			t.Parallel()

			file, err := stageSchema(tc.stage)
			if err != nil {
				t.Fatalf("stageSchema(%s): %v", tc.stage, err)
			}
			doc := readSchema(t, file)

			if got, want := keys(doc.Properties), jsonFields(tc.envelope); !reflect.DeepEqual(got, want) {
				t.Errorf("%s declares properties %v, decoder accepts %v", file, got, want)
			}
			if doc.AdditionalProperties == nil || *doc.AdditionalProperties {
				t.Errorf("%s does not set additionalProperties:false, but its decoder rejects unknown fields", file)
			}
			for _, name := range doc.Required {
				if _, ok := doc.Properties[name]; !ok {
					t.Errorf("%s requires %q, which it does not declare as a property", file, name)
				}
			}
		})
	}
}

// TestReviewFindingsSchemaAndDecoderDeclareTheSameFields covers the one
// nested shape, which drifts the same way for the same reason.
func TestReviewFindingsSchemaAndDecoderDeclareTheSameFields(t *testing.T) {
	t.Parallel()

	doc := readSchema(t, "templates/review.schema.json")
	findings, ok := doc.Properties["findings"]
	if !ok || findings.Items == nil {
		t.Fatal("review.schema.json declares no findings array with an item shape")
	}

	if got, want := keys(findings.Items.Properties), jsonFields(findingEnvelope{}); !reflect.DeepEqual(got, want) {
		t.Errorf("review.schema.json findings items declare %v, decoder accepts %v", got, want)
	}
	if findings.Items.AdditionalProperties == nil || *findings.Items.AdditionalProperties {
		t.Error("review.schema.json findings items do not set additionalProperties:false")
	}
}

// TestEveryStageSchemaRequiresEveryPropertyItDeclares is the regression test
// for #576: review.schema.json declared "verified" as a property but left it
// out of the top-level "required" array, which reads as "optional" to a
// human but is not a shape codex's structured-output mode accepts at all —
// it rejects a schema where "required" omits any declared property before
// spending a single token, on every stage's every turn, not just review's.
//
// verified itself stays semantically optional: a turn that finds nothing to
// name in it answers with an empty array, which costs nothing and branches
// on nothing (see work.ReviewOutput.Verified). That is what "optional" has
// to mean here — every property present in "required", with an empty value
// standing in for "nothing to say" — never a property missing from
// "required" outright.
func TestEveryStageSchemaRequiresEveryPropertyItDeclares(t *testing.T) {
	t.Parallel()

	files := []string{
		"templates/plan.schema.json",
		"templates/implement.schema.json",
		"templates/review.schema.json",
	}
	for _, file := range files {
		t.Run(file, func(t *testing.T) {
			t.Parallel()

			doc := readSchema(t, file)
			required := make(map[string]bool, len(doc.Required))
			for _, name := range doc.Required {
				required[name] = true
			}
			for name := range doc.Properties {
				if !required[name] {
					t.Errorf("%s declares %q but does not require it; codex's structured-output mode rejects a schema where \"required\" omits a declared property", file, name)
				}
			}
		})
	}
}
