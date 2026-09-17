// Package tfwrite reads and edits Terraform configuration as a tree,
// and writes it back preserving everything it was not asked to change.
//
// It is hclwrite narrowed to Terraform. HCL can express anything; a
// Terraform file is a known shape — top-level blocks drawn from a fixed
// set, each taking a known number of labels — so the API talks about
// resources, modules and variables rather than about generic bodies,
// and the parser can reject a block that Terraform itself would.
//
// # Fidelity
//
// Every node records the byte range it came from. Printing walks the
// tree and copies those original bytes for any node that was not
// modified, re-printing only what changed. So a file parsed and written
// back untouched is byte-identical — comments, blank lines, alignment
// and all — and an edited one keeps its formatting everywhere the edit
// did not reach.
//
// Expressions are kept as source text, never evaluated: `var.region`
// stays `var.region`. Terraform resolves those against state and
// variables long after a hook has run, so anything else would be a
// guess.
package tfwrite

import (
	"strings"
)

// File is a parsed Terraform file.
type File struct {
	filename string
	src      []byte
	body     *Body
}

// Body is an ordered sequence of items — attributes, blocks and the
// comments between them. Order is the order in the source, which is
// what keeps a comment next to the thing it describes.
type Body struct {
	items []Item
	// src is the file's original bytes, for nodes that print verbatim.
	src []byte
	// dirty records a change to the body itself — an item added or
	// removed — as opposed to a change inside one of its items.
	dirty bool
}

// Item is one thing in a body: a Comment, an Attribute or a Block.
type Item interface {
	// span is the item's byte range in the original source, or the zero
	// span for an item that was created rather than parsed.
	span() srcSpan
	// print appends this item's text, reusing the original bytes when
	// the item has not been modified.
	print(src []byte, indent int, b *strings.Builder)
}

type srcSpan struct {
	start, end int
	// valid is false for a node built in memory, which has no original
	// text to fall back on.
	valid bool
}

// Comment is a comment line or block standing between other items.
type Comment struct {
	Text string
	sp   srcSpan
}

func (c *Comment) span() srcSpan { return c.sp }

// Attribute is a `name = expression` pair. Expr is the expression's
// source text, unevaluated.
type Attribute struct {
	Name string
	Expr string

	sp      srcSpan
	changed bool
}

func (a *Attribute) span() srcSpan { return a.sp }

// SetExpr replaces the attribute's expression with raw source text —
// `var.region`, `"literal"`, `jsonencode({...})`. The text is written
// as given, so the caller decides whether something is an expression or
// a quoted string.
func (a *Attribute) SetExpr(expr string) {
	a.Expr = expr
	a.changed = true
}

// Block is what every Terraform block implements. The concrete types in
// blocks.go add the labels each one actually has: a Resource has a type
// and a name, a Provider has only a name, Locals has neither. Terraform
// does not give blocks a uniform label shape — `backend "s3"` labels a
// type, `provider_meta "aws"` labels a provider — so a single Name()
// across all of them would be inventing a convention that is not there.
type Block interface {
	Item
	// BlockType is the keyword: "resource", "provider", "locals".
	BlockType() string
	// Labels are the block's labels in source order.
	Labels() []string
	// Body is the block's contents.
	Body() *Body
}

// block is the shared state every concrete block embeds.
type block struct {
	blockType string
	labels    []string
	body      *Body

	sp            srcSpan
	labelsChanged bool
}

func (b *block) span() srcSpan     { return b.sp }
func (b *block) BlockType() string { return b.blockType }
func (b *block) Labels() []string  { return append([]string(nil), b.labels...) }
func (b *block) Body() *Body       { return b.body }

// setLabel writes one label by position. Only the concrete types call
// it, each knowing which position means what for its own block type.
func (b *block) setLabel(i int, v string) {
	for len(b.labels) <= i {
		b.labels = append(b.labels, "")
	}
	b.labels[i] = v
	b.labelsChanged = true
}

func (b *block) label(i int) string {
	if i >= len(b.labels) {
		return ""
	}
	return b.labels[i]
}

// Filename is the name the file was parsed under, for diagnostics.
func (f *File) Filename() string { return f.filename }

// Body returns the file's top-level body.
func (f *File) Body() *Body { return f.body }
