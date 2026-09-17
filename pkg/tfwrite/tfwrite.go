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
	text string
	sp   srcSpan
	// parent is the body holding this item, so it can remove itself.
	parent *Body
}

// Text is the comment including its marker — "# note" — or "" when the
// comment is nil. A reader rather than a field, for the same reason as
// Attribute.Expr.
func (c *Comment) Text() string {
	if c == nil {
		return ""
	}
	return c.text
}

// SetText replaces the comment's text.
func (c *Comment) SetText(text string) {
	if c == nil {
		return
	}
	c.text = text
	c.sp = srcSpan{}
}

// Delete removes this comment from the body holding it, reporting
// whether it was still there. Safe on nil, so the result of a lookup
// that found nothing can be deleted without a guard.
func (c *Comment) Delete() bool {
	if c == nil || c.parent == nil {
		return false
	}
	return c.parent.Remove(c)
}

func (c *Comment) span() srcSpan { return c.sp }

// Attribute is a `name = expression` pair. Expr is the expression's
// source text, unevaluated.
type Attribute struct {
	name string
	expr string

	sp      srcSpan
	changed bool
	parent  *Body
}

// Delete removes this attribute from the body holding it, reporting
// whether it was still there. Safe on nil.
func (a *Attribute) Delete() bool {
	if a == nil || a.parent == nil {
		return false
	}
	return a.parent.Remove(a)
}

func (a *Attribute) span() srcSpan { return a.sp }

// SetExpr replaces the attribute's expression with raw source text —
// `var.region`, `"literal"`, `jsonencode({...})`. The text is written
// as given, so the caller decides whether something is an expression or
// a quoted string.
// Name is the attribute's name, or "" when the attribute is nil.
func (a *Attribute) Name() string {
	if a == nil {
		return ""
	}
	return a.name
}

// Expr is the attribute's expression as source text, unevaluated, or ""
// when the attribute is nil. A reader rather than a field so a chain
// through a lookup that found nothing returns "" instead of panicking.
func (a *Attribute) Expr() string {
	if a == nil {
		return ""
	}
	return a.expr
}

// SetExpr replaces the expression with raw source text.
func (a *Attribute) SetExpr(expr string) {
	if a == nil {
		return
	}
	a.expr = expr
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
	parent        *Body
	// self is the concrete wrapper — *Resource, *Provider — which is
	// what sits in the parent's item list. The embedded block is not
	// itself an Item, so removal has to compare against this.
	self Block
}

// remove is the shared half of Delete. Each concrete type wraps it with
// its own nil check, since a method promoted from this embedded struct
// would dereference a nil block before it could test for one.
func (b *block) remove() bool {
	if b == nil || b.parent == nil {
		return false
	}
	return b.parent.Remove(b.self)
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
func (f *File) Filename() string {
	if f == nil {
		return ""
	}
	return f.filename
}

// Body returns the file's top-level body.
func (f *File) Body() *Body {
	if f == nil {
		return nil
	}
	return f.body
}
