package codec

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/goccy/go-json"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/tmccombs/hcl2json/convert"
	"github.com/zclconf/go-cty/cty"
)

// HCLToJSON converts HCL (Terraform's native syntax) into the JSON shape
// Terraform itself defines for .tf.json, so the two spellings of a
// configuration produce the same object.
//
// Expressions are not evaluated: `var.region` comes back as the string
// "${var.region}", the same way it would be written in JSON syntax.
// Comments are not represented — JSON has nowhere to put them.
func HCLToJSON(src []byte, filename string) ([]byte, error) {
	if filename == "" {
		filename = "main.tf"
	}
	out, err := convert.Bytes(src, filename, convert.Options{})
	if err != nil {
		return nil, fmt.Errorf("parsing HCL: %w", err)
	}
	return out, nil
}

// terraformBlockLabels is how many labels each Terraform block type
// takes, which is what tells a nested object in the JSON shape apart
// from a block header: `resource.aws_s3_bucket.logs` is two labels and
// then a body, while `locals` is a body straight away. Anything not
// listed is written as an attribute.
var terraformBlockLabels = map[string]int{
	"resource":    2,
	"data":        2,
	"module":      1,
	"output":      1,
	"provider":    1,
	"variable":    1,
	"provisioner": 1,
	"check":       1,
	"import":      0,
	"locals":      0,
	"moved":       0,
	"removed":     0,
	"terraform":   0,
}

// wholeInterpolation matches a string that is nothing but one
// interpolation — "${var.region}". Those are expressions that were
// written unquoted in native syntax, so they go back unquoted.
var wholeInterpolation = regexp.MustCompile(`^\$\{([\s\S]*)\}$`)

// JSONToHCL renders the Terraform JSON shape back as native HCL.
//
// It is the inverse of HCLToJSON for structure and values, not for
// presentation: comments are gone (JSON never carried them) and the
// output is canonically formatted rather than as-authored. A string that
// is one whole interpolation is unwrapped back into an expression, so
// "${var.region}" is written as var.region rather than as a quoted
// string that Terraform would then have to re-parse.
func JSONToHCL(data []byte) ([]byte, error) {
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing JSON: %w", err)
	}
	root, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("terraform.stringify: top level must be an object, got %T", doc)
	}

	f := hclwrite.NewEmptyFile()
	body := f.Body()
	for _, key := range sortedKeys(root) {
		labels, isBlock := terraformBlockLabels[key]
		if !isBlock {
			if err := setAttribute(body, key, root[key]); err != nil {
				return nil, err
			}
			continue
		}
		if err := writeBlocks(body, []string{key}, labels, root[key]); err != nil {
			return nil, err
		}
	}
	return hclwrite.Format(f.Bytes()), nil
}

// writeBlocks walks the label levels of one block type, emitting a block
// once the labels run out. A body may be a single object or a list of
// them — Terraform's JSON syntax allows both for repeated blocks.
func writeBlocks(body *hclwrite.Body, header []string, remaining int, node any) error {
	if remaining > 0 {
		level, ok := node.(map[string]any)
		if !ok {
			return fmt.Errorf("terraform.stringify: %s: expected an object of labels, got %T",
				strings.Join(header, "."), node)
		}
		for _, label := range sortedKeys(level) {
			if err := writeBlocks(body, append(header, label), remaining-1, level[label]); err != nil {
				return err
			}
		}
		return nil
	}

	switch v := node.(type) {
	case []any:
		for _, one := range v {
			if err := writeBlocks(body, header, 0, one); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		block := body.AppendNewBlock(header[0], header[1:])
		return writeBody(block.Body(), v)
	default:
		return fmt.Errorf("terraform.stringify: %s: expected a block body, got %T",
			strings.Join(header, "."), node)
	}
}

// writeBody fills a block body. A nested object under a name that is a
// known nested-block spelling stays an attribute — Terraform accepts
// both, and an attribute is the form that survives a round trip
// unambiguously.
func writeBody(body *hclwrite.Body, fields map[string]any) error {
	for _, name := range sortedKeys(fields) {
		if err := setAttribute(body, name, fields[name]); err != nil {
			return err
		}
	}
	return nil
}

func setAttribute(body *hclwrite.Body, name string, value any) error {
	tokens, err := tokensFor(value)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	body.SetAttributeRaw(name, tokens)
	return nil
}

// tokensFor renders one JSON value as HCL. Strings that are a single
// whole interpolation become bare expressions; plain scalars go through
// cty so quoting, escaping and numeric formatting are HCL's own.
//
// Collections are walked here rather than handed to TokensForValue,
// because that would render an interpolation nested inside a map or list
// as the literal "$${...}" — escaped, and no longer the expression it
// started as.
func tokensFor(value any) (hclwrite.Tokens, error) {
	switch v := value.(type) {
	case string:
		if m := wholeInterpolation.FindStringSubmatch(v); m != nil {
			expr := strings.TrimSpace(m[1])
			if isParseableExpression(expr) {
				return hclwrite.Tokens{{Type: hclsyntax.TokenIdent, Bytes: []byte(expr)}}, nil
			}
		}
	case []any:
		out := hclwrite.Tokens{{Type: hclsyntax.TokenOBrack, Bytes: []byte("[")}}
		for i, e := range v {
			if i > 0 {
				out = append(out, &hclwrite.Token{Type: hclsyntax.TokenComma, Bytes: []byte(",")})
			}
			inner, err := tokensFor(e)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out = append(out, inner...)
		}
		return append(out, &hclwrite.Token{Type: hclsyntax.TokenCBrack, Bytes: []byte("]")}), nil
	case map[string]any:
		out := hclwrite.Tokens{
			{Type: hclsyntax.TokenOBrace, Bytes: []byte("{")},
			{Type: hclsyntax.TokenNewline, Bytes: []byte("\n")},
		}
		for _, k := range sortedKeys(v) {
			inner, err := tokensFor(v[k])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out = append(out, objectKeyTokens(k)...)
			out = append(out, &hclwrite.Token{Type: hclsyntax.TokenEqual, Bytes: []byte("=")})
			out = append(out, inner...)
			out = append(out, &hclwrite.Token{Type: hclsyntax.TokenNewline, Bytes: []byte("\n")})
		}
		return append(out, &hclwrite.Token{Type: hclsyntax.TokenCBrace, Bytes: []byte("}")}), nil
	}

	v, err := ctyValue(value)
	if err != nil {
		return nil, err
	}
	return hclwrite.TokensForValue(v), nil
}

// objectKeyTokens writes an object key bare when it is a valid
// identifier and quoted otherwise, which is how HCL itself prints them.
func objectKeyTokens(key string) hclwrite.Tokens {
	if hclsyntax.ValidIdentifier(key) {
		return hclwrite.Tokens{{Type: hclsyntax.TokenIdent, Bytes: []byte(key)}}
	}
	return hclwrite.TokensForValue(cty.StringVal(key))
}

// isParseableExpression guards the unquoting: only unwrap when what is
// inside the interpolation really is an expression, so a literal string
// that merely looks like one cannot produce a broken file.
func isParseableExpression(src string) bool {
	_, diags := hclsyntax.ParseExpression([]byte(src), "expr.hcl", hcl.InitialPos)
	return !diags.HasErrors()
}

func ctyValue(value any) (cty.Value, error) {
	switch v := value.(type) {
	case nil:
		return cty.NullVal(cty.DynamicPseudoType), nil
	case bool:
		return cty.BoolVal(v), nil
	case string:
		return cty.StringVal(v), nil
	case float64:
		return cty.NumberFloatVal(v), nil
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return cty.NilVal, fmt.Errorf("number %q: %w", v.String(), err)
		}
		return cty.NumberFloatVal(f), nil
	case []any:
		if len(v) == 0 {
			return cty.EmptyTupleVal, nil
		}
		elems := make([]cty.Value, 0, len(v))
		for i, e := range v {
			ev, err := ctyValue(e)
			if err != nil {
				return cty.NilVal, fmt.Errorf("[%d]: %w", i, err)
			}
			elems = append(elems, ev)
		}
		return cty.TupleVal(elems), nil
	case map[string]any:
		if len(v) == 0 {
			return cty.EmptyObjectVal, nil
		}
		attrs := make(map[string]cty.Value, len(v))
		for _, k := range sortedKeys(v) {
			ev, err := ctyValue(v[k])
			if err != nil {
				return cty.NilVal, fmt.Errorf("%s: %w", k, err)
			}
			attrs[k] = ev
		}
		return cty.ObjectVal(attrs), nil
	default:
		return cty.NilVal, fmt.Errorf("unsupported value of type %T", value)
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
