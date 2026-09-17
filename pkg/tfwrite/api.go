package tfwrite

import "fmt"

// BlockLabels is how many labels each top-level Terraform block type
// takes. Mirrors configFileSchema in terraform/internal/configs, which
// cannot be imported — it lives under internal/ — so it is copied and
// pinned by a test instead.
//
// `provisioner` is deliberately absent: it looks like it belongs and
// does not, being nested inside a resource rather than top-level.
var BlockLabels = map[string]int{
	"terraform": 0,
	"locals":    0,
	"moved":     0,
	"removed":   0,
	"import":    0,
	"provider":  1,
	"variable":  1,
	"output":    1,
	"module":    1,
	"check":     1,
	"resource":  2,
	"data":      2,
	"ephemeral": 2,
	"action":    2,
}

// ---- reading -----------------------------------------------------------

// Items returns the body's contents in source order.
func (b *Body) Items() []Item { return b.items }

// Blocks returns the body's blocks, optionally filtered to one type.
func (b *Body) Blocks(blockType string) []*Block {
	var out []*Block
	for _, item := range b.items {
		blk, ok := item.(*Block)
		if !ok || (blockType != "" && blk.Type != blockType) {
			continue
		}
		out = append(out, blk)
	}
	return out
}

// Block returns the first block matching a type and labels, or nil.
// Passing fewer labels than the block has matches on the prefix, so
// Block("resource", "aws_s3_bucket") finds the first bucket.
func (b *Body) Block(blockType string, labels ...string) *Block {
	for _, blk := range b.Blocks(blockType) {
		if len(labels) > len(blk.Labels) {
			continue
		}
		match := true
		for i, l := range labels {
			if blk.Labels[i] != l {
				match = false
				break
			}
		}
		if match {
			return blk
		}
	}
	return nil
}

// Attributes returns the body's attributes in source order.
func (b *Body) Attributes() []*Attribute {
	var out []*Attribute
	for _, item := range b.items {
		if attr, ok := item.(*Attribute); ok {
			out = append(out, attr)
		}
	}
	return out
}

// Attribute returns the named attribute, or nil.
func (b *Body) Attribute(name string) *Attribute {
	for _, attr := range b.Attributes() {
		if attr.Name == name {
			return attr
		}
	}
	return nil
}

// Comments returns the body's comments in source order.
func (b *Body) Comments() []*Comment {
	var out []*Comment
	for _, item := range b.items {
		if c, ok := item.(*Comment); ok {
			out = append(out, c)
		}
	}
	return out
}

// ---- Terraform shorthands ---------------------------------------------

// Resources returns every `resource` block, optionally filtered to one
// resource type.
func (f *File) Resources(resourceType string) []*Block {
	return f.byTypeAndFirstLabel("resource", resourceType)
}

// Resource returns `resource "type" "name"`, or nil.
func (f *File) Resource(resourceType, name string) *Block {
	return f.body.Block("resource", resourceType, name)
}

// DataSources returns every `data` block, optionally filtered by type.
func (f *File) DataSources(dataType string) []*Block {
	return f.byTypeAndFirstLabel("data", dataType)
}

// DataSource returns `data "type" "name"`, or nil.
func (f *File) DataSource(dataType, name string) *Block { return f.body.Block("data", dataType, name) }

// Modules returns every `module` block.
func (f *File) Modules() []*Block { return f.body.Blocks("module") }

// Module returns `module "name"`, or nil.
func (f *File) Module(name string) *Block { return f.body.Block("module", name) }

// Variables returns every `variable` block.
func (f *File) Variables() []*Block { return f.body.Blocks("variable") }

// Variable returns `variable "name"`, or nil.
func (f *File) Variable(name string) *Block { return f.body.Block("variable", name) }

// Outputs returns every `output` block.
func (f *File) Outputs() []*Block { return f.body.Blocks("output") }

// Output returns `output "name"`, or nil.
func (f *File) Output(name string) *Block { return f.body.Block("output", name) }

// Providers returns every `provider` block.
func (f *File) Providers() []*Block { return f.body.Blocks("provider") }

// Locals returns every `locals` block. Terraform allows more than one.
func (f *File) Locals() []*Block { return f.body.Blocks("locals") }

// Terraform returns the `terraform` settings block, or nil.
func (f *File) Terraform() *Block { return f.body.Block("terraform") }

func (f *File) byTypeAndFirstLabel(blockType, first string) []*Block {
	all := f.body.Blocks(blockType)
	if first == "" {
		return all
	}
	var out []*Block
	for _, blk := range all {
		if len(blk.Labels) > 0 && blk.Labels[0] == first {
			out = append(out, blk)
		}
	}
	return out
}

// ---- editing -----------------------------------------------------------

// SetAttribute sets an attribute's expression, adding it at the end of
// the body when it is not already there. expr is raw source text.
func (b *Body) SetAttribute(name, expr string) *Attribute {
	if attr := b.Attribute(name); attr != nil {
		attr.SetExpr(expr)
		return attr
	}
	attr := &Attribute{Name: name, Expr: expr, changed: true}
	b.items = append(b.items, attr)
	b.dirty = true
	return attr
}

// RemoveAttribute drops the named attribute, reporting whether it was
// there. A comment sitting above it is left alone: it may have been
// describing the block rather than the attribute, and dropping someone's
// note is worse than leaving one that reads oddly.
func (b *Body) RemoveAttribute(name string) bool {
	for i, item := range b.items {
		attr, ok := item.(*Attribute)
		if !ok || attr.Name != name {
			continue
		}
		b.items = append(b.items[:i], b.items[i+1:]...)
		b.dirty = true
		return true
	}
	return false
}

// AppendBlock adds a block at the end of the body and returns it.
func (b *Body) AppendBlock(blockType string, labels ...string) *Block {
	blk := &Block{
		Type:   blockType,
		Labels: append([]string(nil), labels...),
		body:   &Body{src: b.src, dirty: true},
	}
	b.items = append(b.items, blk)
	b.dirty = true
	return blk
}

// RemoveBlock drops the first block matching a type and labels,
// reporting whether it was there.
func (b *Body) RemoveBlock(blockType string, labels ...string) bool {
	target := b.Block(blockType, labels...)
	if target == nil {
		return false
	}
	for i, item := range b.items {
		if item == Item(target) {
			b.items = append(b.items[:i], b.items[i+1:]...)
			b.dirty = true
			return true
		}
	}
	return false
}

// AppendComment adds a comment at the end of the body.
func (b *Body) AppendComment(text string) *Comment {
	c := &Comment{Text: text}
	b.items = append(b.items, c)
	b.dirty = true
	return c
}

// AppendBlock on a File is shorthand for the top-level body, and
// rejects a block type Terraform does not have or a label count it would
// not accept — the mistakes that otherwise surface as a file Terraform
// refuses to load.
func (f *File) AppendBlock(blockType string, labels ...string) (*Block, error) {
	want, known := BlockLabels[blockType]
	if !known {
		return nil, fmt.Errorf("tfwrite: %q is not a Terraform block type", blockType)
	}
	if len(labels) != want {
		return nil, fmt.Errorf("tfwrite: %s takes %d label(s), got %d", blockType, want, len(labels))
	}
	return f.body.AppendBlock(blockType, labels...), nil
}
