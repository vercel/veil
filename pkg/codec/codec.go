// Package codec reads and writes veil's on-disk documents. It owns the
// JSON/YAML dispatch — format detected from the content when decoding,
// from the path extension when encoding — so every reader and writer in
// veil agrees on how a document maps to a Go value.
//
// Every entry point takes `any`. Values implementing proto.Message are
// routed through protojson (see proto.go) so proto field naming
// (snake_case) and well-known-type shapes are honored; everything else
// goes through goccy/go-json and yaml.v3, which cannot reflect into
// proto types. The proto branch lives inside each codec rather than in
// a parallel set of Proto* entry points, so callers pick a function by
// what they are doing (decode a reader, write a file) and never by what
// their value happens to be.
package codec

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

// Decoder decodes one document from an underlying reader. The format
// is fixed when the Decoder is built; the proto/generic choice is made
// per value at Decode time.
type Decoder interface {
	Decode(v any) error
}

// Encoder encodes one document to an underlying writer. Mirrors
// Decoder: format fixed at construction, proto/generic chosen per
// value.
type Encoder interface {
	Encode(v any) error
}

// NewDecoder peeks the first non-whitespace byte of r and returns the
// matching decoder: '{' or '[' -> JSON, anything else (or empty input)
// -> YAML. Content-based detection so a JSON document served at a URL
// with no extension, or piped over stdin, decodes correctly without
// callers having to tell us the format.
func NewDecoder(r io.Reader) Decoder {
	br, isJSON := IsJSON(r)
	if isJSON {
		return &jsonDecoder{r: br}
	}
	return &yamlDecoder{d: yaml.NewDecoder(br)}
}

// NewEncoder returns the encoder implied by p's extension
// (.yaml/.yml -> yaml.v3, anything else -> indented JSON). p names the
// destination document; it is used for the format choice only and is
// never opened.
func NewEncoder(p string, w io.Writer) Encoder {
	if IsYAML(p) {
		return &yamlEncoder{e: yaml.NewEncoder(w)}
	}
	return &jsonEncoder{w: w}
}

// Decode reads one JSON or YAML document from r into v. One-shot form
// of NewDecoder + Decode.
func Decode(r io.Reader, v any) error {
	return NewDecoder(r).Decode(v)
}

// Unmarshal decodes JSON bytes into v. Proto targets go through
// protojson with DiscardUnknown, so editor-injected `$schema` fields
// don't break loading.
func Unmarshal(data []byte, v any) error {
	if m, ok := v.(proto.Message); ok {
		return ProtoUnmarshal.Unmarshal(data, m)
	}
	return json.Unmarshal(data, v)
}

// Marshal encodes v as compact JSON. Proto sources go through
// protojson with UseProtoNames, keeping field names snake_case
// (matching the .proto source).
func Marshal(v any) ([]byte, error) {
	if m, ok := v.(proto.Message); ok {
		return ProtoMarshal.Marshal(m)
	}
	return json.Marshal(v)
}

// Convert re-encodes src and decodes the result into dst. Each end
// picks its own encoding independently, which makes this the canonical
// way to cross the proto/generic boundary: narrowing a
// google.protobuf.Value into a typed message, or flattening a proto
// into the generic map a hook context round-trips through.
func Convert(src, dst any) error {
	raw, err := Marshal(src)
	if err != nil {
		return fmt.Errorf("marshalling: %w", err)
	}
	if err := Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("re-parsing: %w", err)
	}
	return nil
}

// jsonDecoder decodes JSON into the target. The proto branch reads the
// whole input because protojson works on byte slices, not io.Readers,
// and we handle one document per reader; empty input leaves the
// message at its zero value rather than erroring, since a proto is
// populated in place and an absent document is not a parse failure.
type jsonDecoder struct {
	r io.Reader
}

func (d *jsonDecoder) Decode(v any) error {
	if m, ok := v.(proto.Message); ok {
		raw, err := io.ReadAll(d.r)
		if err != nil {
			return fmt.Errorf("reading JSON: %w", err)
		}
		if len(raw) == 0 {
			return nil
		}
		return ProtoUnmarshal.Unmarshal(raw, m)
	}
	return json.NewDecoder(d.r).Decode(v)
}

// yamlDecoder decodes YAML into the target. The proto branch decodes
// one document into a generic `any`, JSON-encodes it via goccy/go-json,
// then hands that to protojson: yaml.v3 has no idea how to populate
// proto-defined shapes — notably structpb.Value's polymorphic oneof —
// so the YAML -> JSON intermediate gives protojson the document in the
// form it understands.
type yamlDecoder struct {
	d *yaml.Decoder
}

func (d *yamlDecoder) Decode(v any) error {
	m, isProto := v.(proto.Message)
	if !isProto {
		return d.d.Decode(v)
	}
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
	return ProtoUnmarshal.Unmarshal(raw, m)
}

// jsonEncoder writes indented JSON. A trailing newline matches the
// stdjson.Encoder convention so files end cleanly at EOF. The generic
// branch leaves HTML unescaped so a `<` in a description survives a
// read/modify/write round-trip as itself.
type jsonEncoder struct {
	w io.Writer
}

func (e *jsonEncoder) Encode(v any) error {
	if m, ok := v.(proto.Message); ok {
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
	enc := json.NewEncoder(e.w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// yamlEncoder writes YAML. Protos are flattened through protojson into
// a generic `any` first — same reason as yamlDecoder: yaml.v3 can't
// reflect into proto types, so it only ever sees the canonical document
// shape protojson produces.
type yamlEncoder struct {
	e *yaml.Encoder
}

func (e *yamlEncoder) Encode(v any) error {
	defer e.e.Close()
	if _, ok := v.(proto.Message); ok {
		var doc any
		if err := Convert(v, &doc); err != nil {
			return err
		}
		v = doc
	}
	return e.e.Encode(v)
}

// ReadFile opens p and decodes the contents into v.
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

// EncodeWriter writes v to w in the format implied by p's extension.
// One-shot form of NewEncoder + Encode.
func EncodeWriter(p string, w io.Writer, v any) error {
	if err := NewEncoder(p, w).Encode(v); err != nil {
		return fmt.Errorf("encoding %s: %w", p, err)
	}
	return nil
}

// WriteFileAny encodes v to p in the format implied by its extension,
// so a document read from YAML is written back as YAML.
func WriteFileAny(p string, v any) error {
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return EncodeWriter(p, f, v)
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
