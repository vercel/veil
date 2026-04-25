// Package embeds provides embedded static assets for the veil CLI.
package embeds

import _ "embed"

// VeilConfigDefinitionSchema is the dereferenced JSON Schema for the
// hand-authored veil.json (VeilConfigDefinition).
//
//go:embed jsonschema/VeilConfigDefinition.schema.json
var VeilConfigDefinitionSchema []byte

// KindDefinitionSchema is the dereferenced JSON Schema for the
// hand-authored kind.json (KindDefinition).
//
//go:embed jsonschema/KindDefinition.schema.json
var KindDefinitionSchema []byte

// ResourceSchema is the dereferenced JSON Schema for Resource.
//
//go:embed jsonschema/Resource.schema.json
var ResourceSchema []byte

// MetadataSchema is the dereferenced JSON Schema for Metadata.
//
//go:embed jsonschema/Metadata.schema.json
var MetadataSchema []byte

// KindSchema is the dereferenced JSON Schema for the published, compiled
// Kind document (the bundled output of `veil build`).
//
//go:embed jsonschema/Kind.schema.json
var KindSchema []byte
