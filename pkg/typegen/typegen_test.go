package typegen

import (
	"testing"

	"github.com/stretchr/testify/suite"
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
