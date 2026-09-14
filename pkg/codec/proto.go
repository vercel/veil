package codec

// This file is the proto specialization of the codec: the protojson
// configuration veil uses for every on-disk proto document — kind.json,
// registry.json, resources, veil.json — plus the buf.validate runtime.
// Keeping the options in one place makes sure every reader and writer
// agrees on field naming (snake_case via UseProtoNames) and read-side
// forgiveness (DiscardUnknown, so editor-injected `$schema` fields
// don't break loading).

import (
	"fmt"
	"sync"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var (
	// ProtoMarshal is the canonical protojson marshaller. UseProtoNames
	// keeps field names snake_case (matching the .proto source). The
	// generic Marshal routes proto values here.
	ProtoMarshal = protojson.MarshalOptions{UseProtoNames: true}

	// PrettyMarshal is ProtoMarshal with two-space indentation for
	// human-readable output. Used by the JSON Encoder so on-disk
	// documents are diff-friendly.
	PrettyMarshal = protojson.MarshalOptions{UseProtoNames: true, Indent: "  "}

	// ProtoUnmarshal is the canonical protojson unmarshaller.
	// DiscardUnknown keeps reads forgiving when users carry editor
	// metadata like `$schema` in their JSON. The generic Unmarshal
	// routes proto targets here.
	ProtoUnmarshal = protojson.UnmarshalOptions{DiscardUnknown: true}
)

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
// immediately after decoding so any constraint violation surfaces with
// the same error path as the decode itself. It stays typed to
// proto.Message rather than taking `any`: constraints only exist on
// proto definitions, and silently skipping validation for a non-proto
// value would be worse than a compile error.
func Validate(m proto.Message) error {
	v, err := getValidator()
	if err != nil {
		return fmt.Errorf("initializing validator: %w", err)
	}
	return v.Validate(m)
}
