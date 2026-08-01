package agenttool

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/0x63616c/software-factory/internal/agent"
)

// ToolsetID identifies one immutable meaning of a tool catalogue.
type ToolsetID = agent.ToolsetID

type runtimeTool interface {
	Specification() Specification
	Execute(context.Context, json.RawMessage) (Result, error)
}

// Set is an immutable versioned catalogue of tools.
type Set struct {
	id             ToolsetID
	tools          map[string]runtimeTool
	specifications []Specification
	fingerprint    string
}

// MustSet constructs a versioned tool catalogue or panics when its contract is invalid.
func MustSet(id ToolsetID, tools ...runtimeTool) Set {
	if strings.TrimSpace(string(id)) == "" {
		panic("agenttool: toolset id is blank")
	}
	set := Set{
		id:             id,
		tools:          make(map[string]runtimeTool, len(tools)),
		specifications: make([]Specification, 0, len(tools)),
	}
	for _, tool := range tools {
		if isNilRuntimeTool(tool) {
			panic(fmt.Sprintf("agenttool: nil tool in toolset %q", id))
		}
		specification := tool.Specification()
		if strings.TrimSpace(specification.Name) == "" {
			panic(fmt.Sprintf("agenttool: tool name is blank in toolset %q", id))
		}
		if strings.TrimSpace(specification.Description) == "" {
			panic(fmt.Sprintf("agenttool: tool %q description is blank in toolset %q", specification.Name, id))
		}
		if err := validatePropertyDescriptions(specification.Parameters); err != nil {
			panic(fmt.Sprintf("agenttool: tool %q schema: %v", specification.Name, err))
		}
		if _, exists := set.tools[specification.Name]; exists {
			panic(fmt.Sprintf("agenttool: duplicate tool %q in toolset %q", specification.Name, id))
		}
		set.tools[specification.Name] = tool
		set.specifications = append(set.specifications, specification)
	}
	sort.Slice(set.specifications, func(i, j int) bool {
		return set.specifications[i].Name < set.specifications[j].Name
	})
	canonical, err := json.Marshal(struct {
		ID             ToolsetID       `json:"id"`
		Specifications []Specification `json:"specifications"`
	}{ID: id, Specifications: set.specifications})
	if err != nil {
		panic(fmt.Sprintf("agenttool: fingerprint toolset %q: %v", id, err))
	}
	set.fingerprint = fmt.Sprintf("sha256:%x", sha256.Sum256(canonical))
	return set
}

func validatePropertyDescriptions(schemaJSON []byte) error {
	var schema map[string]any
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	rootType, _ := schema["type"].(string)
	if rootType != "object" {
		return fmt.Errorf("root type is %q, want object", rootType)
	}
	if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
		return fmt.Errorf("root permits arbitrary object keys")
	}
	properties, _ := schema["properties"].(map[string]any)
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		property, ok := properties[name].(map[string]any)
		if !ok {
			return fmt.Errorf("property %q schema is not an object", name)
		}
		description, _ := property["description"].(string)
		if strings.TrimSpace(description) == "" {
			return fmt.Errorf("property %q description is blank", name)
		}
		if propertyType, _ := property["type"].(string); propertyType == "object" {
			if additional, ok := property["additionalProperties"].(bool); !ok || additional {
				return fmt.Errorf("property %q permits arbitrary object keys", name)
			}
		}
	}
	return nil
}

func isNilRuntimeTool(tool runtimeTool) bool {
	if tool == nil {
		return true
	}
	value := reflect.ValueOf(tool)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	case reflect.Invalid, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128,
		reflect.Array, reflect.String, reflect.Struct, reflect.UnsafePointer:
		return false
	}
	panic(fmt.Sprintf("unhandled reflect kind %s", value.Kind()))
}

// Specifications returns model-facing definitions in deterministic name order.
func (s Set) Specifications() []Specification {
	specifications := make([]Specification, len(s.specifications))
	for index, specification := range s.specifications {
		specifications[index] = Specification{
			Name:        specification.Name,
			Description: specification.Description,
			Parameters:  append(json.RawMessage(nil), specification.Parameters...),
		}
	}
	return specifications
}

// Fingerprint returns the stable digest of the toolset identity and schemas.
func (s Set) Fingerprint() string {
	return s.fingerprint
}

// ID returns the immutable toolset identity.
func (s Set) ID() ToolsetID {
	return s.id
}

// Execute dispatches provider arguments to the named typed tool.
func (s Set) Execute(ctx context.Context, name string, arguments json.RawMessage) (Result, error) {
	tool, ok := s.tools[name]
	if !ok {
		return Result{Content: fmt.Sprintf("unknown tool %q", name), IsError: true}, nil
	}
	return tool.Execute(ctx, arguments)
}
