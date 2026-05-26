// Package protoencode is a generic bytes <-> proto encoder/decoder.
// It owns the JSON / YAML on-disk dispatch and the shared protojson
// configuration; it has no opinion on what's inside the documents.
package protoencode

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/goccy/go-json"

	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

// Decoder decodes one document from an underlying reader into a
// proto.Message. Both the JSON and YAML implementations go through
// protojson so proto field tags (snake_case names, well-known types
// like google.protobuf.Value's polymorphic oneof) are honored —
// yaml.v3 / encoding/json can't reflect into proto types directly.
type Decoder interface {
	Decode(v proto.Message) error
}

// Encoder encodes one proto.Message to an underlying writer. Mirrors
// Decoder: both JSON and YAML branches start from protojson so the
// emitted document uses proto field naming and well-known-type
// shapes.
type Encoder interface {
	Encode(m proto.Message) error
}

// IsJSON peeks past leading whitespace on r and reports whether the
// next byte starts a JSON document ('{' or '['). It returns a reader
// that replays the bytes IsJSON consumed during the peek, so callers
// hand the returned reader (not r) to the chosen decoder. Read errors
// during the peek collapse to `false`; the downstream decoder will
// rediscover the same error with proper context. Empty input also
// reports false — empty files are valid (null) YAML.
func IsJSON(r io.Reader) (io.Reader, bool) {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}
	for {
		b, err := br.Peek(1)
		if err != nil {
			return br, false
		}
		switch b[0] {
		case ' ', '\t', '\n', '\r':
			if _, err := br.Discard(1); err != nil {
				return br, false
			}
			continue
		case '{', '[':
			return br, true
		default:
			return br, false
		}
	}
}

// NewDecoder peeks the first non-whitespace byte of r and returns the
// matching proto decoder: '{' or '[' -> JSON, anything else (or empty
// input) -> YAML. Content-based detection so a JSON document served
// at a URL with no extension, or piped over stdin, decodes correctly
// without callers having to tell us the format.
func NewDecoder(r io.Reader) Decoder {
	br, isJSON := IsJSON(r)
	if isJSON {
		return &jsonDecoder{r: br}
	}
	return &yamlDecoder{d: yaml.NewDecoder(br)}
}

// jsonDecoder reads the rest of the input and protojson-unmarshals
// into the target proto. We read-all + Unmarshal rather than using a
// streaming json.Decoder because protojson works on byte slices, not
// io.Readers, and the documents we handle are one per reader.
type jsonDecoder struct {
	r io.Reader
}

func (d *jsonDecoder) Decode(m proto.Message) error {
	raw, err := io.ReadAll(d.r)
	if err != nil {
		return fmt.Errorf("reading JSON: %w", err)
	}
	if len(raw) == 0 {
		return nil
	}
	return Unmarshal.Unmarshal(raw, m)
}

// yamlDecoder decodes one YAML document into a generic `any`,
// JSON-encodes it via goccy/go-json, then protojson-unmarshals into
// the target proto. The round-trip exists because yaml.v3 has no idea
// how to populate proto-defined shapes — notably structpb.Value's
// polymorphic oneof — so the YAML -> JSON intermediate hands protojson
// the document in the form it understands.
type yamlDecoder struct {
	d *yaml.Decoder
}

func (d *yamlDecoder) Decode(m proto.Message) error {
	var doc any
	if err := d.d.Decode(&doc); err != nil {
		if err == io.EOF {
			return nil
		}
		return fmt.Errorf("decoding YAML: %w", err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("re-encoding YAML as JSON for protojson: %w", err)
	}
	return Unmarshal.Unmarshal(raw, m)
}

// jsonEncoder writes m as indented JSON via PrettyMarshal. A trailing
// newline matches the stdjson.Encoder convention so files end cleanly
// at EOF.
type jsonEncoder struct {
	w io.Writer
}

func (e *jsonEncoder) Encode(m proto.Message) error {
	raw, err := PrettyMarshal.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshalling: %w", err)
	}
	if _, err := e.w.Write(raw); err != nil {
		return err
	}
	_, err = e.w.Write([]byte{'\n'})
	return err
}

// yamlEncoder writes m as YAML by marshalling through protojson into
// a generic `any` and then encoding that with yaml.v3. Same reason as
// yamlDecoder: yaml.v3 can't reflect into proto types directly, so we
// hand it the canonical document shape protojson produces.
type yamlEncoder struct {
	e *yaml.Encoder
}

func (e *yamlEncoder) Encode(m proto.Message) error {
	raw, err := Marshal.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshalling: %w", err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("re-parsing: %w", err)
	}
	return e.e.Encode(doc)
}

// UnmarshalProto decodes one document from r into m. One-shot form of
// NewDecoder + Decode.
func UnmarshalProto(r io.Reader, m proto.Message) error {
	return NewDecoder(r).Decode(m)
}

// Decode is the non-proto, generic decode path: read one document
// from r into v (any). Format is detected via IsJSON. For proto
// messages use NewDecoder / UnmarshalProto so protojson handles
// proto-specific field naming.
func Decode(r io.Reader, v any) error {
	br, isJSON := IsJSON(r)
	if isJSON {
		return json.NewDecoder(br).Decode(v)
	}
	return yaml.NewDecoder(br).Decode(v)
}

// ReadFile opens p and decodes the contents into v (any). For protos
// see ReadProtoFile.
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

// EncodeWriter writes v (any) to w in the format implied by p's
// extension (.yaml/.yml -> yaml.v3, anything else -> indented JSON).
// For protos use NewEncoder.
func EncodeWriter(p string, w io.Writer, v any) error {
	if IsYAML(p) {
		enc := yaml.NewEncoder(w)
		defer enc.Close()
		if err := enc.Encode(v); err != nil {
			return fmt.Errorf("encoding %s: %w", p, err)
		}
		return nil
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encoding %s: %w", p, err)
	}
	return nil
}

// WriteFileAny encodes v (any) to path in the format implied by the
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
