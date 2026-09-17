package tfwrite

import (
	"fmt"

	"github.com/goccy/go-json"
)

// The tree crosses into a hook as plain JSON rather than as a handle the
// hook calls back into. A handle would mean a host round trip per
// attribute read, across a Wasm boundary, for a document that is already
// small enough to hand over whole. As data it is also just an object the
// hook can walk with the language it already has.
//
// Spans travel with it. A node that comes back with its span intact and
// `changed` unset is printed from the original bytes, so the fidelity
// guarantee survives the trip; a node the hook touched is marked and
// reprinted.

type jsonItem struct {
	Kind    string      `json:"kind"`
	Text    string      `json:"text,omitempty"`
	Name    string      `json:"name,omitempty"`
	Expr    string      `json:"expr,omitempty"`
	Type    string      `json:"type,omitempty"`
	Labels  []string    `json:"labels,omitempty"`
	Items   []*jsonItem `json:"items,omitempty"`
	Start   int         `json:"start,omitempty"`
	End     int         `json:"end,omitempty"`
	Spanned bool        `json:"spanned"`
	Changed bool        `json:"changed,omitempty"`
}

type jsonFile struct {
	Filename string      `json:"filename"`
	Items    []*jsonItem `json:"items"`
	Dirty    bool        `json:"dirty,omitempty"`
}

// MarshalTree renders the tree as JSON for a caller outside Go. The
// source is not included: the caller cannot print, and the host still
// holds the bytes that unmodified nodes print from.
func (f *File) MarshalTree() ([]byte, error) {
	if f == nil {
		return nil, fmt.Errorf("tfwrite: MarshalTree on a nil File")
	}
	return json.Marshal(&jsonFile{
		Filename: f.filename,
		Items:    marshalItems(f.body.items),
		Dirty:    f.body.dirty,
	})
}

func marshalItems(items []Item) []*jsonItem {
	out := make([]*jsonItem, 0, len(items))
	for _, item := range items {
		switch v := item.(type) {
		case *Comment:
			out = append(out, &jsonItem{
				Kind: "comment", Text: v.text,
				Start: v.sp.start, End: v.sp.end, Spanned: v.sp.valid,
			})
		case *Attribute:
			out = append(out, &jsonItem{
				Kind: "attribute", Name: v.name, Expr: v.expr,
				Start: v.sp.start, End: v.sp.end, Spanned: v.sp.valid, Changed: v.changed,
			})
		case Block:
			b := blockBase(v)
			out = append(out, &jsonItem{
				Kind: "block", Type: b.blockType, Labels: b.labels,
				Items: marshalItems(b.body.items),
				Start: b.sp.start, End: b.sp.end, Spanned: b.sp.valid,
				Changed: b.labelsChanged || b.body.dirty,
			})
		}
	}
	return out
}

// UnmarshalTree rebuilds a File from a tree a caller edited, against the
// source it was parsed from. src has to be the same bytes — the spans in
// the tree index into it, and printing an unmodified node means copying
// from it.
func UnmarshalTree(src []byte, data []byte) (*File, error) {
	var doc jsonFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("tfwrite: parsing tree: %w", err)
	}
	f := &File{filename: doc.Filename, src: src}
	body, err := unmarshalBody(src, doc.Items, doc.Dirty)
	if err != nil {
		return nil, err
	}
	f.body = body
	return f, nil
}

func unmarshalBody(src []byte, items []*jsonItem, dirty bool) (*Body, error) {
	b := &Body{src: src, dirty: dirty}
	for _, it := range items {
		sp := srcSpan{start: it.Start, end: it.End, valid: it.Spanned}
		if sp.valid && (sp.start < 0 || sp.end > len(src) || sp.start > sp.end) {
			return nil, fmt.Errorf("tfwrite: %s: span [%d,%d) is outside the source", it.Kind, sp.start, sp.end)
		}
		switch it.Kind {
		case "comment":
			c := &Comment{text: it.Text, sp: sp, parent: b}
			b.items = append(b.items, c)
		case "attribute":
			b.items = append(b.items, &Attribute{
				name: it.Name, expr: it.Expr, sp: sp, changed: it.Changed, parent: b,
			})
		case "block":
			inner, err := unmarshalBody(src, it.Items, it.Changed)
			if err != nil {
				return nil, err
			}
			blk := newBlock(it.Type, it.Labels, inner, sp)
			base := blockBase(blk)
			base.labelsChanged, base.parent = it.Changed, b
			b.items = append(b.items, blk)
		default:
			return nil, fmt.Errorf("tfwrite: unknown item kind %q", it.Kind)
		}
	}
	return b, nil
}
