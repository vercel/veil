// Package typegen generates TypeScript interfaces from JSON Schema.
package typegen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/goccy/go-json"
)

var unmarshal = json.Unmarshal

// Schema is a simplified JSON Schema representation for type generation.
type Schema struct {
	Type                 string            `json:"type"`
	Properties           map[string]Schema `json:"properties,omitempty"`
	Required             []string          `json:"required,omitempty"`
	Items                *Schema           `json:"items,omitempty"`
	AdditionalProperties *Schema           `json:"additionalProperties,omitempty"`
	Description          string            `json:"description,omitempty"`
	Enum                 []any             `json:"enum,omitempty"`
	Default              any               `json:"default,omitempty"`
	HasDefault           bool              `json:"-"`
}

// schemaRaw is the wire-shape that UnmarshalJSON feeds into — it owns
// the same fields as Schema minus the computed HasDefault flag, with
// `Default` as *any so we can distinguish "absent" from "null", and
// `AdditionalProperties` as json.RawMessage so a bool or schema value
// (both legal per JSON Schema) round-trips without erroring.
type schemaRaw struct {
	Type                 string            `json:"type"`
	Properties           map[string]Schema `json:"properties,omitempty"`
	Required             []string          `json:"required,omitempty"`
	Items                *Schema           `json:"items,omitempty"`
	AdditionalProperties json.RawMessage   `json:"additionalProperties,omitempty"`
	Description          string            `json:"description,omitempty"`
	Enum                 []any             `json:"enum,omitempty"`
	Default              *any              `json:"default,omitempty"`
}

// UnmarshalJSON flags any declared `default` (even `null`) so we can
// tell "default present" from "default absent" — a plain `any` field
// can't distinguish those two states. `additionalProperties` accepts
// either a boolean (ignored for type generation) or a schema object
// (used to infer `Record<string, T>` types).
func (s *Schema) UnmarshalJSON(data []byte) error {
	var r schemaRaw
	if err := unmarshal(data, &r); err != nil {
		return err
	}
	s.Type = r.Type
	s.Properties = r.Properties
	s.Required = r.Required
	s.Items = r.Items
	s.Description = r.Description
	s.Enum = r.Enum
	if r.Default != nil {
		s.Default = *r.Default
		s.HasDefault = true
	}
	// additionalProperties may be `true`/`false` or a schema. We only
	// care about the schema form (it drives Record<string, T> typing);
	// the boolean form carries no type info and is ignored.
	if len(r.AdditionalProperties) > 0 && r.AdditionalProperties[0] == '{' {
		var sub Schema
		if err := unmarshal(r.AdditionalProperties, &sub); err != nil {
			return err
		}
		s.AdditionalProperties = &sub
	}
	return nil
}

// GenerateInterface produces a TypeScript interface from a JSON Schema object.
func GenerateInterface(name string, s Schema) string {
	var b strings.Builder
	if s.Description != "" {
		b.WriteString(formatComment(s.Description, ""))
	}
	b.WriteString(fmt.Sprintf("export interface %s {\n", name))

	required := toSet(s.Required)
	keys := sortedKeys(s.Properties)

	for _, key := range keys {
		prop := s.Properties[key]
		// Fields marked as required OR with a default are always present
		// at render time (required is explicit; defaults are filled in
		// by the render pipeline before hooks see the spec), so they
		// generate as non-optional TS properties.
		optional := "?"
		if required[key] || prop.HasDefault {
			optional = ""
		}
		if prop.Description != "" {
			b.WriteString(formatComment(prop.Description, "  "))
		}
		b.WriteString(fmt.Sprintf("  %s%s: %s;\n", key, optional, tsType(prop)))
	}

	b.WriteString("}\n")
	return b.String()
}

// tsType converts a JSON Schema property to a TypeScript type string.
func tsType(s Schema) string {
	if len(s.Enum) > 0 {
		return enumType(s.Enum)
	}

	switch s.Type {
	case "string":
		return "string"
	case "number", "integer":
		return "number"
	case "boolean":
		return "boolean"
	case "array":
		if s.Items != nil {
			return tsType(*s.Items) + "[]"
		}
		return "unknown[]"
	case "object":
		if s.AdditionalProperties != nil {
			return fmt.Sprintf("Record<string, %s>", tsType(*s.AdditionalProperties))
		}
		if len(s.Properties) > 0 {
			// Inline object type.
			var b strings.Builder
			b.WriteString("{ ")
			keys := sortedKeys(s.Properties)
			required := toSet(s.Required)
			for i, key := range keys {
				prop := s.Properties[key]
				optional := "?"
				if required[key] || prop.HasDefault {
					optional = ""
				}
				if i > 0 {
					b.WriteString("; ")
				}
				b.WriteString(fmt.Sprintf("%s%s: %s", key, optional, tsType(prop)))
			}
			b.WriteString(" }")
			return b.String()
		}
		return "Record<string, unknown>"
	default:
		return "unknown"
	}
}

func enumType(vals []any) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = fmt.Sprintf("%q", fmt.Sprintf("%v", v))
	}
	return strings.Join(parts, " | ")
}

func formatComment(desc string, indent string) string {
	lines := strings.Split(desc, "\n")
	if len(lines) == 1 {
		return fmt.Sprintf("%s/** %s */\n", indent, desc)
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s/**\n", indent))
	for _, line := range lines {
		b.WriteString(fmt.Sprintf("%s * %s\n", indent, strings.TrimRight(line, " ")))
	}
	b.WriteString(fmt.Sprintf("%s */\n", indent))
	return b.String()
}

func sortedKeys(m map[string]Schema) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}
