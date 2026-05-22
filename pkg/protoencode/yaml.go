package protoencode

import (
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

// decoderFunc reads a single document from r and writes it into v.
// One per registered extension; lookups go through `decoders`.
type decoderFunc func(r io.Reader, v any) error

// encoderFunc writes v to w in the format the caller has chosen.
// One per registered extension; lookups go through `encoders`.
type encoderFunc func(w io.Writer, v any) error

// decoders maps file extension (lowercased, including the leading
// dot) to the decoder used for that format. Unknown extensions fall
// back to JSON via `decoderFor`.
var decoders = map[string]decoderFunc{
	".json": decodeJSON,
	".yaml": decodeYAML,
	".yml":  decodeYAML,
}

// encoders is the write-side companion to `decoders`.
var encoders = map[string]encoderFunc{
	".json": encodeJSON,
	".yaml": encodeYAML,
	".yml":  encodeYAML,
}

func decodeJSON(r io.Reader, v any) error {
	return stdjson.NewDecoder(r).Decode(v)
}

func decodeYAML(r io.Reader, v any) error {
	return yaml.NewDecoder(r).Decode(v)
}

func encodeJSON(w io.Writer, v any) error {
	enc := stdjson.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func encodeYAML(w io.Writer, v any) error {
	enc := yaml.NewEncoder(w)
	defer enc.Close()
	return enc.Encode(v)
}

// IsYAML reports whether p names a YAML document (.yaml or .yml,
// case-insensitive). HTTP(S) URLs have their query string stripped
// before the check so `https://host/registry.yaml?token=x` is still
// recognized.
func IsYAML(p string) bool {
	return extOf(p) == ".yaml" || extOf(p) == ".yml"
}

// extOf returns p's lowercased extension. For HTTP(S) URLs the
// path component is extracted first so trailing query/fragment
// strings don't poison the suffix.
func extOf(p string) string {
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		if u, err := url.Parse(p); err == nil {
			p = u.Path
		}
	}
	return strings.ToLower(path.Ext(p))
}

// decoderFor returns the decoder registered for p's extension, or
// the JSON decoder when no extension matches — JSON is the default
// for unknown suffixes (legacy local paths, HTTP URLs without an
// explicit extension) so existing callers keep working.
func decoderFor(p string) decoderFunc {
	if dec, ok := decoders[extOf(p)]; ok {
		return dec
	}
	return decodeJSON
}

// encoderFor is the write-side companion to `decoderFor`.
func encoderFor(p string) encoderFunc {
	if enc, ok := encoders[extOf(p)]; ok {
		return enc
	}
	return encodeJSON
}

// DecodeReader decodes one document from r into v using the decoder
// implied by p's extension. Use this when bytes don't live on disk
// (HTTP response bodies, in-memory buffers); for files on disk
// prefer ReadFile / ReadFS, which open the file for you.
func DecodeReader(p string, r io.Reader, v any) error {
	if err := decoderFor(p)(r, v); err != nil {
		return fmt.Errorf("decoding %s: %w", p, err)
	}
	return nil
}

// EncodeWriter encodes v to w using the encoder implied by p's
// extension. Companion to DecodeReader.
func EncodeWriter(p string, w io.Writer, v any) error {
	if err := encoderFor(p)(w, v); err != nil {
		return fmt.Errorf("encoding %s: %w", p, err)
	}
	return nil
}

// ReadFile opens path and decodes one document into v using the
// decoder implied by the extension: .yaml/.yml dispatch to yaml.v3,
// .json (or anything unrecognized) to encoding/json. For proto
// messages, use ReadProtoFile — protojson's snake_case + unknown-
// field handling is what veil's protos expect, and this generic
// helper can't honor that without going through JSON bytes first.
func ReadFile(p string, v any) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return DecodeReader(p, f, v)
}

// ReadFS is the fs.FS variant of ReadFile.
func ReadFS(fsys fs.FS, p string, v any) error {
	f, err := fsys.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return DecodeReader(p, f, v)
}

// WriteFileAny encodes v to path in the format implied by the
// extension: JSON gets two-space indent and a trailing newline,
// YAML uses yaml.v3 (which sorts keys alphabetically and strips
// comments). Named with the "Any" suffix because the existing
// WriteFile takes a proto.Message and the two would clash otherwise.
func WriteFileAny(p string, v any) error {
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return EncodeWriter(p, f, v)
}

// ReadProtoFile reads path and unmarshals one document into m via
// protojson. YAML sources are decoded with yaml.v3 then re-encoded
// as JSON so protojson's UseProtoNames / DiscardUnknown semantics
// still apply — there's no shortcut here because yaml.v3 doesn't
// know about proto field annotations.
func ReadProtoFile(p string, m proto.Message) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return DecodeProto(p, f, m)
}

// ReadProtoFS is the fs.FS variant of ReadProtoFile.
func ReadProtoFS(fsys fs.FS, p string, m proto.Message) error {
	f, err := fsys.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return DecodeProto(p, f, m)
}

// DecodeProto decodes one document from r into m. JSON inputs go
// straight to protojson; YAML inputs are decoded as YAML then
// re-encoded as JSON before protojson sees them. Exposed so callers
// that already hold an io.Reader (registry HTTP fetches, embedded
// resources) can reuse the same dispatch.
func DecodeProto(p string, r io.Reader, m proto.Message) error {
	return DecodeProtoWithRewrite(p, r, m, nil)
}

// ReadProtoFileWithRewrite is the rewriting variant of ReadProtoFile:
// the source is decoded into a generic map first, handed to `rewrite`
// for in-place mutation, and only then unmarshalled into m via the
// shared protojson configuration. Use this when the on-disk shape
// accepts shorthand or extension fields that protojson can't see —
// the rewrite step turns them into something the proto can decode
// without callers having to glue together
// `ReadFile + json.Marshal + Unmarshal.Unmarshal` by hand.
//
// `rewrite` may be nil, in which case this is identical to
// ReadProtoFile.
func ReadProtoFileWithRewrite(p string, m proto.Message, rewrite func(map[string]any) error) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return DecodeProtoWithRewrite(p, f, m, rewrite)
}

// ReadProtoFSWithRewrite is the fs.FS variant of
// ReadProtoFileWithRewrite.
func ReadProtoFSWithRewrite(fsys fs.FS, p string, m proto.Message, rewrite func(map[string]any) error) error {
	f, err := fsys.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return DecodeProtoWithRewrite(p, f, m, rewrite)
}

// DecodeProtoWithRewrite decodes one document from r into a generic
// map (handling both JSON and YAML via the extension on p), invokes
// `rewrite` to mutate the map in place if non-nil, then marshals the
// result through protojson into m.
//
// When `rewrite` is nil and p is .json, the original byte stream is
// passed straight to protojson — saves the round-trip through a
// generic map for the common case.
func DecodeProtoWithRewrite(p string, r io.Reader, m proto.Message, rewrite func(map[string]any) error) error {
	// Fast path: a JSON source with no rewrite can go straight to
	// protojson without round-tripping through a generic map.
	if rewrite == nil && !IsYAML(p) {
		bytes, err := io.ReadAll(r)
		if err != nil {
			return fmt.Errorf("reading %s: %w", p, err)
		}
		return Unmarshal.Unmarshal(bytes, m)
	}

	var doc map[string]any
	if err := DecodeReader(p, r, &doc); err != nil {
		return err
	}
	if rewrite != nil {
		if err := rewrite(doc); err != nil {
			return err
		}
	}
	bytes, err := stdjson.Marshal(doc)
	if err != nil {
		return fmt.Errorf("re-encoding %s as JSON for protojson: %w", p, err)
	}
	return Unmarshal.Unmarshal(bytes, m)
}
