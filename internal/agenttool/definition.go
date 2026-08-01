// Package agenttool defines typed tools exposed to an agent.
package agenttool

import (
	"encoding/json"
	"fmt"

	reflectschema "github.com/invopop/jsonschema"
	validateschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// Specification is the model-facing definition of a tool.
type Specification struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Definition binds a tool name and description to one Go input type.
type Definition[T any] struct {
	specification Specification
	validator     *validateschema.Schema
}

// Define derives a strict JSON schema from T once at construction time.
func Define[T any](name, description string) Definition[T] {
	reflector := reflectschema.Reflector{
		Anonymous:      true,
		DoNotReference: true,
	}
	var input T
	schema := reflector.Reflect(input)
	schema.Version = ""
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		panic(fmt.Sprintf("agenttool: marshal schema for %q: %v", name, err))
	}

	return Definition[T]{
		specification: Specification{
			Name:        name,
			Description: description,
			Parameters:  schemaJSON,
		},
		validator: compileSchema(name, schemaJSON),
	}
}

func compileSchema(name string, schemaJSON []byte) *validateschema.Schema {
	var document any
	if err := json.Unmarshal(schemaJSON, &document); err != nil {
		panic(fmt.Sprintf("agenttool: decode schema for %q: %v", name, err))
	}
	compiler := validateschema.NewCompiler()
	compiler.DefaultDraft(validateschema.Draft2020)
	const location = "urn:agenttool:schema"
	if err := compiler.AddResource(location, document); err != nil {
		panic(fmt.Sprintf("agenttool: add schema for %q: %v", name, err))
	}
	validator, err := compiler.Compile(location)
	if err != nil {
		panic(fmt.Sprintf("agenttool: compile schema for %q: %v", name, err))
	}
	return validator
}

// Specification returns the immutable model-facing tool definition.
func (d Definition[T]) Specification() Specification {
	return Specification{
		Name:        d.specification.Name,
		Description: d.specification.Description,
		Parameters:  append(json.RawMessage(nil), d.specification.Parameters...),
	}
}
