// Package embeds provides embedded static assets for the veil CLI.
package embeds

import _ "embed"

// VeilConfigSchema is the dereferenced JSON Schema for VeilConfig.
//
//go:embed jsonschema/VeilConfig.schema.json
var VeilConfigSchema []byte

// KindSchema is the dereferenced JSON Schema for Kind.
//
//go:embed jsonschema/Kind.schema.json
var KindSchema []byte

// ResourceSchema is the dereferenced JSON Schema for Resource.
//
//go:embed jsonschema/Resource.schema.json
var ResourceSchema []byte

// MetadataSchema is the dereferenced JSON Schema for Metadata.
//
//go:embed jsonschema/Metadata.schema.json
var MetadataSchema []byte

// CompiledKindSchema is the dereferenced JSON Schema for CompiledKind.
//
//go:embed jsonschema/CompiledKind.schema.json
var CompiledKindSchema []byte
