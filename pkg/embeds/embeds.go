// Package embeds provides embedded static assets for the veil CLI.
package embeds

import _ "embed"

// SchemaURLBase is the GitHub raw-content prefix for the canonical JSON
// Schemas in this repo. Generated JSON files emit `$schema` references
// pointing under this prefix so editors and validators can fetch the
// authoritative definitions without a checked-out clone.
const SchemaURLBase = "https://raw.githubusercontent.com/vercel/veil/main/api/jsonschema"

// Schema URLs for each generator's output, pinned to the published
// schemas under api/jsonschema/.
const (
	VeilConfigDefinitionSchemaURL = SchemaURLBase + "/VeilConfigDefinition.schema.json"
	KindDefinitionSchemaURL       = SchemaURLBase + "/KindDefinition.schema.json"
	KindSchemaURL                 = SchemaURLBase + "/Kind.schema.json"
	RegistrySchemaURL             = SchemaURLBase + "/Registry.schema.json"
)

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
