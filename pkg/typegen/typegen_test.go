package typegen

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/vercel/veil/pkg/tsc"
)

type TypegenSuite struct {
	suite.Suite
}

func TestTypegenSuite(t *testing.T) {
	suite.Run(t, new(TypegenSuite))
}

func (s *TypegenSuite) TestGenerateInterfaceBasicTypes() {
	schema := Schema{
		Type: "object",
		Properties: map[string]Schema{
			"port":     {Type: "number", Description: "The port your service listens on."},
			"replicas": {Type: "integer", Description: "Number of replicas to run."},
			"image":    {Type: "string"},
		},
		Required: []string{"port"},
	}

	result := GenerateInterface("ServiceSpec", schema)
	s.Contains(result, "export interface ServiceSpec {")
	s.Contains(result, "  port: number;")
	s.Contains(result, "  replicas?: number;")
	s.Contains(result, "  image?: string;")
	s.Contains(result, "/** The port your service listens on. */")
}

func (s *TypegenSuite) TestGenerateInterfaceWithMap() {
	schema := Schema{
		Type: "object",
		Properties: map[string]Schema{
			"env": {
				Type:                 "object",
				AdditionalProperties: &Schema{Type: "string"},
			},
		},
	}

	result := GenerateInterface("Spec", schema)
	s.Contains(result, "  env?: Record<string, string>;")
}

func (s *TypegenSuite) TestGenerateInterfaceWithArray() {
	schema := Schema{
		Type: "object",
		Properties: map[string]Schema{
			"tags": {
				Type:  "array",
				Items: &Schema{Type: "string"},
			},
		},
	}

	result := GenerateInterface("Spec", schema)
	s.Contains(result, "  tags?: string[];")
}

func (s *TypegenSuite) TestGenerateInterfaceWithEnum() {
	schema := Schema{
		Type: "object",
		Properties: map[string]Schema{
			"protocol": {
				Type: "string",
				Enum: []any{"tcp", "udp"},
			},
		},
	}

	result := GenerateInterface("Spec", schema)
	s.Contains(result, `  protocol?: "tcp" | "udp";`)
}

func (s *TypegenSuite) TestUnionTypes() {
	checker := tsc.Find()
	if checker == nil {
		s.T().Skip("no tsc/tsgo on PATH")
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		s.Run(keyword, func() {
			var schema Schema
			s.Require().NoError(unmarshal([]byte(fmt.Sprintf(`{
				"type": "object",
				"required": ["datadog", "values", "entries", "choice"],
				"properties": {
					"datadog": {"type": "object", "properties": {
						"trace_sample_rate": {"%[1]s": [{"type": "string"}, {"type": "null"}], "default": "0.1"},
						"runtime_metrics_enabled": {"%[1]s": [{"type": "boolean"}, {"type": "null"}]}
					}},
					"values": {"type": "array", "items": {"%[1]s": [{"type": "string"}, {"type": "number"}]}},
					"entries": {"type": "object", "additionalProperties": {"%[1]s": [{"type": "boolean"}, {"type": "null"}]}},
					"choice": {"%[1]s": [
						{"type": "object", "required": ["kind", "value"], "properties": {"kind": {"enum": ["text"]}, "value": {"type": "string"}}},
						{"type": "object", "required": ["kind", "value"], "properties": {"kind": {"enum": ["count"]}, "value": {"type": "number"}}}
					]}
				}
			}`, keyword)), &schema))
			generated := GenerateInterface("Spec", schema)
			generated += GenerateInterface("Choice", schema.Properties["choice"])
			s.Contains(generated, "trace_sample_rate: string | null")
			s.Contains(generated, "runtime_metrics_enabled?: boolean | null")
			path := filepath.Join(s.T().TempDir(), "unions.ts")
			s.Require().NoError(os.WriteFile(path, []byte(generated+`
declare const spec: Spec;
const choice: Choice = { kind: "count", value: 42 };
// @ts-expect-error Root unions retain the branch value type.
const badChoice: Choice = { kind: "count", value: "text" };
const rate: string | null = spec.datadog.trace_sample_rate;
const metrics: boolean | null | undefined = spec.datadog.runtime_metrics_enabled;
spec.datadog.trace_sample_rate = null;
spec.datadog.runtime_metrics_enabled = false;
spec.values = ["text", 42];
const values: (string | number)[] = spec.values;
spec.entries = { enabled: true, absent: null };
const entry: boolean | null = spec.entries.enabled;
if (spec.choice.kind === "text") {
  const text: string = spec.choice.value;
} else {
  const count: number = spec.choice.value;
}
// @ts-expect-error Numbers are not tracing sample-rate strings.
spec.datadog.trace_sample_rate = 42;
// @ts-expect-error The array itself cannot be a scalar.
spec.values = "text";
// @ts-expect-error Boolean elements are outside the union.
spec.values = [true];
// @ts-expect-error The discriminator must agree with the value.
spec.choice = { kind: "text", value: 42 };
`), 0644))
			s.Require().NoError(checker.Check([]string{path}))
		})
	}
}

func (s *TypegenSuite) TestUnionSiblingConstraints() {
	checker := tsc.Find()
	if checker == nil {
		s.T().Skip("no tsc/tsgo on PATH")
	}
	var schema Schema
	s.Require().NoError(unmarshal([]byte(`{
		"type": "string",
		"anyOf": [{"type": "string"}, {"type": "number"}],
		"oneOf": [{"enum": ["on"]}, {"enum": ["off"]}]
	}`), &schema))
	path := filepath.Join(s.T().TempDir(), "constraints.ts")
	s.Require().NoError(os.WriteFile(path, []byte(GenerateInterface("Mode", schema)+`
const on: Mode = "on";
const off: Mode = "off";
// @ts-expect-error The sibling type excludes numbers.
const number: Mode = 42;
// @ts-expect-error Both composition keywords must hold.
const other: Mode = "other";
`), 0644))
	s.Require().NoError(checker.Check([]string{path}))
}
