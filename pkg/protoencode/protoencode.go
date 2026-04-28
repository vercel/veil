// Package protoencode centralizes the protojson configuration veil uses
// for every on-disk proto document — kind.json, registry.json, resources,
// veil.json. Keeping the options in one place makes sure every
// reader/writer agrees on field naming (snake_case via UseProtoNames)
// and read-side forgiveness (DiscardUnknown so editor-injected `$schema`
// fields don't break loading).
package protoencode

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"os"
	"sync"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Marshal is the canonical protojson marshaller. UseProtoNames keeps
// field names snake_case (matching the .proto source).
var Marshal = protojson.MarshalOptions{UseProtoNames: true}

// Unmarshal is the canonical protojson unmarshaller. DiscardUnknown
// keeps reads forgiving when users carry editor metadata like `$schema`
// in their JSON.
var Unmarshal = protojson.UnmarshalOptions{DiscardUnknown: true}

// validator is the lazily initialized buf.validate runtime evaluator.
// It reads the `(buf.validate.field)` annotations on each .proto field
// and enforces them after unmarshal — keeping the proto file as the
// single source of truth for input validation.
var (
	validatorOnce sync.Once
	validatorInst protovalidate.Validator
	validatorErr  error
)

func getValidator() (protovalidate.Validator, error) {
	validatorOnce.Do(func() {
		validatorInst, validatorErr = protovalidate.New()
	})
	return validatorInst, validatorErr
}

// Validate runs the buf.validate constraints declared on m's proto
// definition against the populated message. Callers invoke this
// immediately after `Unmarshal.Unmarshal` so any constraint violation
// surfaces with the same error path as the unmarshal itself.
func Validate(m proto.Message) error {
	v, err := getValidator()
	if err != nil {
		return fmt.Errorf("initializing validator: %w", err)
	}
	return v.Validate(m)
}

// WriteFile marshals m via protojson and writes it indented to path.
// Output round-trips through a generic map so the result uses canonical
// JSON formatting — protojson always injects an extra space after colons
// (a deliberate non-canonical marker) which is ugly. The re-marshal
// sorts object keys alphabetically; that's stable across runs and fine
// for veil's compiled artifacts.
//
// When schemaURL is non-empty, a `$schema` field is injected at the top
// level so editors can resolve the published schema for the document.
// `$schema` sorts ahead of every proto field name (the leading `$` is
// ASCII 0x24, before any letter), so it lands first in the output.
func WriteFile(path string, m proto.Message, schemaURL string) error {
	raw, err := Marshal.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshalling %s: %w", path, err)
	}
	var generic any
	if err := stdjson.Unmarshal(raw, &generic); err != nil {
		return fmt.Errorf("re-parsing %s: %w", path, err)
	}
	if schemaURL != "" {
		if obj, ok := generic.(map[string]any); ok {
			obj["$schema"] = schemaURL
		}
	}
	var buf bytes.Buffer
	enc := stdjson.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
