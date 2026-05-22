// Package protoencode is a generic bytes <-> proto encoder/decoder.
// It owns the JSON / YAML on-disk dispatch and the shared protojson
// configuration; it has no opinion on what's inside the documents.
package protoencode

import (
	"bufio"
	stdjson "encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"strings"

	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

// Decoder is the format-agnostic decode surface. encoding/json.Decoder
// and yaml.v3.Decoder both satisfy it; callers don't care which.
type Decoder interface {
	Decode(v any) error
}

// NewDecoder peeks the first non-whitespace byte of r and returns the
// matching decoder: '{' or '[' -> JSON, anything else (or empty
// input) -> YAML. Content-based detection so a JSON document served
// at a URL with no extension, or piped over stdin, decodes
// correctly without callers having to tell us the format.
//
// The bufio.Reader the peek consumes is the one the returned decoder
// reads from, so no bytes are lost: the format-detection byte stays
// available for the chosen parser.
func NewDecoder(r io.Reader) (Decoder, error) {
	br := bufio.NewReader(r)
	for {
		b, err := br.Peek(1)
		if err == io.EOF {
			// Empty input is a valid (null) YAML document; the JSON
			// decoder would error on it. Default to YAML so empty
			// files decode cleanly to a zero value.
			return yaml.NewDecoder(br), nil
		}
		if err != nil {
			return nil, fmt.Errorf("peeking decoder format: %w", err)
		}
		switch b[0] {
		case ' ', '\t', '\n', '\r':
			if _, err := br.Discard(1); err != nil {
				return nil, err
			}
			continue
		case '{', '[':
			return stdjson.NewDecoder(br), nil
		default:
			return yaml.NewDecoder(br), nil
		}
	}
}

// Decode is the one-shot form of NewDecoder + Decode: detect format,
// decode a single document into v.
func Decode(r io.Reader, v any) error {
	dec, err := NewDecoder(r)
	if err != nil {
		return err
	}
	return dec.Decode(v)
}

// UnmarshalProto decodes one document from r into m via protojson.
// JSON sources go straight to protojson; YAML sources are decoded
// with yaml.v3 and re-encoded as JSON first because protojson is
// the only protobuf JSON unmarshaller that understands proto field
// tags (snake_case names, well-known types, etc.).
func UnmarshalProto(r io.Reader, m proto.Message) error {
	br := bufio.NewReader(r)
	for {
		b, err := br.Peek(1)
		if err == io.EOF {
			// Empty input -> leave m at its zero value.
			return nil
		}
		if err != nil {
			return fmt.Errorf("peeking decoder format: %w", err)
		}
		switch b[0] {
		case ' ', '\t', '\n', '\r':
			if _, err := br.Discard(1); err != nil {
				return err
			}
			continue
		case '{', '[':
			bytes, err := io.ReadAll(br)
			if err != nil {
				return fmt.Errorf("reading JSON: %w", err)
			}
			return Unmarshal.Unmarshal(bytes, m)
		default:
			var doc any
			if err := yaml.NewDecoder(br).Decode(&doc); err != nil {
				return fmt.Errorf("decoding YAML: %w", err)
			}
			bytes, err := stdjson.Marshal(doc)
			if err != nil {
				return fmt.Errorf("re-encoding YAML as JSON for protojson: %w", err)
			}
			return Unmarshal.Unmarshal(bytes, m)
		}
	}
}

// ReadFile opens p and decodes the contents into v. Format is
// detected by NewDecoder from the bytes — the extension is only used
// to enrich error messages.
func ReadFile(p string, v any) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := Decode(f, v); err != nil {
		return fmt.Errorf("decoding %s: %w", p, err)
	}
	return nil
}

// ReadFS is the fs.FS variant of ReadFile.
func ReadFS(fsys fs.FS, p string, v any) error {
	f, err := fsys.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := Decode(f, v); err != nil {
		return fmt.Errorf("decoding %s: %w", p, err)
	}
	return nil
}

// ReadProtoFile opens p and unmarshals the contents into m via
// protojson (with YAML round-tripped through JSON when needed).
func ReadProtoFile(p string, m proto.Message) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := UnmarshalProto(f, m); err != nil {
		return fmt.Errorf("decoding %s: %w", p, err)
	}
	return nil
}

// ReadProtoFS is the fs.FS variant of ReadProtoFile.
func ReadProtoFS(fsys fs.FS, p string, m proto.Message) error {
	f, err := fsys.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := UnmarshalProto(f, m); err != nil {
		return fmt.Errorf("decoding %s: %w", p, err)
	}
	return nil
}

// EncodeWriter writes v to w in the format implied by p's extension
// (.yaml/.yml -> yaml.v3, anything else -> indented JSON). Encoding
// stays extension-keyed because, unlike decoding, the caller has to
// choose a target format up front.
func EncodeWriter(p string, w io.Writer, v any) error {
	if IsYAML(p) {
		enc := yaml.NewEncoder(w)
		defer enc.Close()
		if err := enc.Encode(v); err != nil {
			return fmt.Errorf("encoding %s: %w", p, err)
		}
		return nil
	}
	enc := stdjson.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encoding %s: %w", p, err)
	}
	return nil
}

// WriteFileAny encodes v to path in the format implied by the
// extension. Named with the "Any" suffix because the existing
// WriteFile takes a proto.Message and the two would clash otherwise.
func WriteFileAny(p string, v any) error {
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return EncodeWriter(p, f, v)
}

// IsYAML reports whether p names a YAML document (.yaml or .yml,
// case-insensitive). HTTP(S) URLs have their query string stripped
// before the check so `https://host/registry.yaml?token=x` is still
// recognized. Used by the encode-side dispatch and by callers that
// need to make a format choice ahead of opening the file.
func IsYAML(p string) bool {
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		if u, err := url.Parse(p); err == nil {
			p = u.Path
		}
	}
	ext := strings.ToLower(path.Ext(p))
	return ext == ".yaml" || ext == ".yml"
}
