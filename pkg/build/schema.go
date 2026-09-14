package build

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/goccy/go-json"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/vercel/veil/pkg/codec"
)

// ValidateSourceContents checks a schema-declared source's contents
// against its JSON Schema. `veil build` runs this so a source that
// violates its own schema fails once at build time, rather than
// silently compiling and then failing the pre-render gate on every
// machine that renders it.
//
// path names the source relative to the kind directory — it picks the
// parse codec and labels the error. contents is the raw file; schemaJSON
// the raw schema document.
func ValidateSourceContents(path string, contents, schemaJSON []byte) error {
	var schemaDoc any
	if err := json.Unmarshal(schemaJSON, &schemaDoc); err != nil {
		return fmt.Errorf("schema: invalid JSON: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	uri := "mem://source/" + path
	if err := compiler.AddResource(uri, schemaDoc); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	sch, err := compiler.Compile(uri)
	if err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	doc, err := ParseSourceContents(path, contents)
	if err != nil {
		return fmt.Errorf("parsing: %w", err)
	}
	if err := sch.Validate(doc); err != nil {
		return errors.New(stripSourceSchemaURL(path, err.Error()))
	}
	return nil
}

// ParseSourceContents decodes a source file into a generic value,
// choosing the codec from path's extension rather than sniffing the
// bytes. Extension, not content, because that is the rule the render
// runtime's typed File accessors use when they parse and re-serialize a
// source — build has to agree with render or a document could validate
// in one and not the other.
func ParseSourceContents(path string, contents []byte) (any, error) {
	var v any
	if codec.IsYAML(path) {
		if err := yaml.Unmarshal(contents, &v); err != nil {
			return nil, err
		}
		return v, nil
	}
	if err := json.NewDecoder(bytes.NewReader(contents)).Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// stripSourceSchemaURL rewrites the in-memory schema URI the validator
// embeds in every message into a human label, matching how the registry
// reports the same failure at render time.
func stripSourceSchemaURL(path, msg string) string {
	re := regexp.MustCompile(`'mem://source/` + regexp.QuoteMeta(path) + `#?[^']*'`)
	label := fmt.Sprintf("source %q schema", path)
	msg = re.ReplaceAllString(msg, label)
	return strings.TrimPrefix(msg, fmt.Sprintf("jsonschema validation failed with %s\n", label))
}
