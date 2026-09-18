package tfwrite

// ---- reading a body ----------------------------------------------------

// Items returns the body's contents in source order.
func (b *Body) Items() []Item {
	if b == nil {
		return nil
	}
	return b.items
}

// Blocks returns every block in the body, in source order. Filter with
// the typed accessors on File rather than by string where you can.
func (b *Body) Blocks() []Block {
	if b == nil {
		return nil
	}
	var out []Block
	for _, item := range b.items {
		if blk, ok := item.(Block); ok {
			out = append(out, blk)
		}
	}
	return out
}

// blocksOfType is the shared lookup behind the typed accessors: every
// block whose keyword matches and whose leading labels match those
// given. A label given as "" matches anything in that position.
func (b *Body) blocksOfType(blockType string, labels ...string) []Block {
	if b == nil {
		return nil
	}
	var out []Block
	for _, blk := range b.Blocks() {
		if blk.BlockType() != blockType {
			continue
		}
		have := blk.Labels()
		if len(labels) > len(have) {
			continue
		}
		match := true
		for i, want := range labels {
			if want != "" && have[i] != want {
				match = false
				break
			}
		}
		if match {
			out = append(out, blk)
		}
	}
	return out
}

// Block returns the first block of a type whose leading labels match,
// or nil. For a top-level block prefer the typed accessors on File; this
// is for the nested ones, whose shape belongs to their parent.
func (b *Body) Block(blockType string, labels ...string) Block {
	return b.firstOfType(blockType, labels...)
}

func (b *Body) firstOfType(blockType string, labels ...string) Block {
	if b == nil {
		return nil
	}
	found := b.blocksOfType(blockType, labels...)
	if len(found) == 0 {
		return nil
	}
	return found[0]
}

// Attributes returns the body's attributes in source order.
func (b *Body) Attributes() []*Attribute {
	if b == nil {
		return nil
	}
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
	if b == nil {
		return nil
	}
	for _, attr := range b.Attributes() {
		if attr.name == name {
			return attr
		}
	}
	return nil
}

// Comments returns the body's comments in source order.
func (b *Body) Comments() []*Comment {
	if b == nil {
		return nil
	}
	var out []*Comment
	for _, item := range b.items {
		if c, ok := item.(*Comment); ok {
			out = append(out, c)
		}
	}
	return out
}

// ---- editing a body ----------------------------------------------------

// SetAttribute sets an attribute's expression, appending it when it is
// not already present. expr is raw source text, so the caller decides
// between a literal and an expression.
func (b *Body) SetAttribute(name, expr string) *Attribute {
	if b == nil {
		return nil
	}
	if attr := b.Attribute(name); attr != nil {
		attr.SetExpr(expr)
		return attr
	}
	attr := &Attribute{name: name, expr: expr, changed: true, parent: b}
	b.items = append(b.items, attr)
	b.dirty = true
	return attr
}

// RemoveAttribute drops the named attribute, reporting whether it was
// there. A comment above it is left alone: it may describe the block
// rather than the attribute, and dropping someone's note is worse than
// leaving one that reads oddly.
func (b *Body) RemoveAttribute(name string) bool {
	if b == nil {
		return false
	}
	for i, item := range b.items {
		attr, ok := item.(*Attribute)
		if !ok || attr.name != name {
			continue
		}
		b.items = append(b.items[:i], b.items[i+1:]...)
		b.dirty = true
		return true
	}
	return false
}

// RenameAttribute renames an attribute in place, keeping its position
// and its expression. Reports false when there is no such attribute, or
// when the new name is already taken — renaming onto a sibling would
// leave the body with two attributes of one name, which Terraform
// rejects and which no later call could tell apart.
func (b *Body) RenameAttribute(from, to string) bool {
	if b == nil || from == to {
		return false
	}
	attr := b.Attribute(from)
	if attr == nil || b.Attribute(to) != nil {
		return false
	}
	attr.SetName(to)
	return true
}

// AppendComment adds a comment at the end of the body.
func (b *Body) AppendComment(text string) *Comment {
	if b == nil {
		return nil
	}
	c := &Comment{text: text, parent: b}
	b.items = append(b.items, c)
	b.dirty = true
	return c
}

// Remove drops any item — a block, an attribute or a comment —
// reporting whether it was there. The typed accessors return the thing
// to pass in, so removing a resource is Remove(f.Resource(...)) and
// removing a comment is Remove(c) for one out of Comments().
func (b *Body) Remove(target Item) bool {
	if b == nil || target == nil {
		return false
	}
	for i, item := range b.items {
		if item != target {
			continue
		}
		b.items = append(b.items[:i], b.items[i+1:]...)
		b.dirty = true
		return true
	}
	return false
}

// RemoveBlock drops a block. Remove does the same for any item; this
// reads better at a call site that has a block in hand.
func (b *Body) RemoveBlock(target Block) bool {
	if target == nil {
		return false
	}
	return b.Remove(target)
}

// RemoveComment drops a comment, the counterpart to AppendComment.
func (b *Body) RemoveComment(target *Comment) bool {
	if target == nil {
		return false
	}
	return b.Remove(target)
}

// appendBlock adds a block of the given shape and returns it.
func (b *Body) appendBlock(blockType string, labels ...string) Block {
	if b == nil {
		return nil
	}
	blk := newBlock(blockType, append([]string(nil), labels...), &Body{src: b.src, dirty: true}, srcSpan{})
	blockBase(blk).parent = b
	b.items = append(b.items, blk)
	b.dirty = true
	return blk
}

// AddBlock adds a block inside this body — lifecycle, connection,
// validation, provisioner and the rest, whose shapes belong to their
// parent rather than to the file.
func (b *Body) AddBlock(blockType string, labels ...string) *Generic {
	if b == nil {
		return nil
	}
	blk := newBlock(blockType, append([]string(nil), labels...), &Body{src: b.src, dirty: true}, srcSpan{})
	blockBase(blk).parent = b
	b.items = append(b.items, blk)
	b.dirty = true
	if g, ok := blk.(*Generic); ok {
		return g
	}
	// A modelled keyword used as a nested block still reads generically.
	return &Generic{*blockBase(blk)}
}

// ---- the typed accessors -----------------------------------------------
//
// One pair per block type, each taking exactly the labels that block
// has. The lookup returns nil when there is no such block; the Add form
// always returns one. There is no way to ask for a resource with one
// label or a provider with two, which is the point of modelling them
// separately rather than passing a slice.

// Resource returns `resource "type" "name"`, or nil.
func (f *File) Resource(resourceType, name string) *Resource {
	if f == nil {
		return nil
	}
	blk := f.body.firstOfType("resource", resourceType, name)
	if blk == nil {
		return nil
	}
	return blk.(*Resource)
}

// Resources returns every resource, or every one of a type when
// resourceType is given.
func (f *File) Resources(resourceType string) []*Resource {
	if f == nil {
		return nil
	}
	var out []*Resource
	for _, blk := range f.body.blocksOfType("resource", resourceType) {
		out = append(out, blk.(*Resource))
	}
	return out
}

// AddResource appends `resource "type" "name"`.
func (f *File) AddResource(resourceType, name string) *Resource {
	if f == nil {
		return nil
	}
	return f.body.appendBlock("resource", resourceType, name).(*Resource)
}

// DataSource returns `data "type" "name"`, or nil.
func (f *File) DataSource(dataType, name string) *DataSource {
	if f == nil {
		return nil
	}
	blk := f.body.firstOfType("data", dataType, name)
	if blk == nil {
		return nil
	}
	return blk.(*DataSource)
}

// DataSources returns every data block, optionally filtered by type.
func (f *File) DataSources(dataType string) []*DataSource {
	if f == nil {
		return nil
	}
	var out []*DataSource
	for _, blk := range f.body.blocksOfType("data", dataType) {
		out = append(out, blk.(*DataSource))
	}
	return out
}

// AddDataSource appends `data "type" "name"`.
func (f *File) AddDataSource(dataType, name string) *DataSource {
	if f == nil {
		return nil
	}
	return f.body.appendBlock("data", dataType, name).(*DataSource)
}

// Provider returns `provider "name"`, or nil. One label, not two: the
// provider's local name.
func (f *File) Provider(name string) *Provider {
	if f == nil {
		return nil
	}
	blk := f.body.firstOfType("provider", name)
	if blk == nil {
		return nil
	}
	return blk.(*Provider)
}

// Providers returns every provider block. More than one may share a
// name, distinguished by `alias`.
func (f *File) Providers() []*Provider {
	if f == nil {
		return nil
	}
	var out []*Provider
	for _, blk := range f.body.blocksOfType("provider") {
		out = append(out, blk.(*Provider))
	}
	return out
}

// AddProvider appends `provider "name"`.
func (f *File) AddProvider(name string) *Provider {
	if f == nil {
		return nil
	}
	return f.body.appendBlock("provider", name).(*Provider)
}

// Variable returns `variable "name"`, or nil.
func (f *File) Variable(name string) *Variable {
	if f == nil {
		return nil
	}
	blk := f.body.firstOfType("variable", name)
	if blk == nil {
		return nil
	}
	return blk.(*Variable)
}

// Variables returns every variable block.
func (f *File) Variables() []*Variable {
	if f == nil {
		return nil
	}
	var out []*Variable
	for _, blk := range f.body.blocksOfType("variable") {
		out = append(out, blk.(*Variable))
	}
	return out
}

// AddVariable appends `variable "name"`.
func (f *File) AddVariable(name string) *Variable {
	if f == nil {
		return nil
	}
	return f.body.appendBlock("variable", name).(*Variable)
}

// Output returns `output "name"`, or nil.
func (f *File) Output(name string) *Output {
	if f == nil {
		return nil
	}
	blk := f.body.firstOfType("output", name)
	if blk == nil {
		return nil
	}
	return blk.(*Output)
}

// Outputs returns every output block.
func (f *File) Outputs() []*Output {
	if f == nil {
		return nil
	}
	var out []*Output
	for _, blk := range f.body.blocksOfType("output") {
		out = append(out, blk.(*Output))
	}
	return out
}

// AddOutput appends `output "name"`.
func (f *File) AddOutput(name string) *Output {
	if f == nil {
		return nil
	}
	return f.body.appendBlock("output", name).(*Output)
}

// Module returns `module "name"`, or nil.
func (f *File) Module(name string) *Module {
	if f == nil {
		return nil
	}
	blk := f.body.firstOfType("module", name)
	if blk == nil {
		return nil
	}
	return blk.(*Module)
}

// Modules returns every module block.
func (f *File) Modules() []*Module {
	if f == nil {
		return nil
	}
	var out []*Module
	for _, blk := range f.body.blocksOfType("module") {
		out = append(out, blk.(*Module))
	}
	return out
}

// AddModule appends `module "name"`.
func (f *File) AddModule(name string) *Module {
	if f == nil {
		return nil
	}
	return f.body.appendBlock("module", name).(*Module)
}

// Check returns `check "name"`, or nil.
func (f *File) Check(name string) *Check {
	if f == nil {
		return nil
	}
	blk := f.body.firstOfType("check", name)
	if blk == nil {
		return nil
	}
	return blk.(*Check)
}

// Checks returns every check block.
func (f *File) Checks() []*Check {
	if f == nil {
		return nil
	}
	var out []*Check
	for _, blk := range f.body.blocksOfType("check") {
		out = append(out, blk.(*Check))
	}
	return out
}

// AddCheck appends `check "name"`.
func (f *File) AddCheck(name string) *Check {
	if f == nil {
		return nil
	}
	return f.body.appendBlock("check", name).(*Check)
}

// Ephemeral returns `ephemeral "type" "name"`, or nil.
func (f *File) Ephemeral(ephemeralType, name string) *Ephemeral {
	if f == nil {
		return nil
	}
	blk := f.body.firstOfType("ephemeral", ephemeralType, name)
	if blk == nil {
		return nil
	}
	return blk.(*Ephemeral)
}

// AddEphemeral appends `ephemeral "type" "name"`.
func (f *File) AddEphemeral(ephemeralType, name string) *Ephemeral {
	if f == nil {
		return nil
	}
	return f.body.appendBlock("ephemeral", ephemeralType, name).(*Ephemeral)
}

// Action returns `action "type" "name"`, or nil.
func (f *File) Action(actionType, name string) *Action {
	if f == nil {
		return nil
	}
	blk := f.body.firstOfType("action", actionType, name)
	if blk == nil {
		return nil
	}
	return blk.(*Action)
}

// AddAction appends `action "type" "name"`.
func (f *File) AddAction(actionType, name string) *Action {
	if f == nil {
		return nil
	}
	return f.body.appendBlock("action", actionType, name).(*Action)
}

// Terraform returns the `terraform` settings block, or nil. No labels.
func (f *File) Terraform() *Terraform {
	if f == nil {
		return nil
	}
	blk := f.body.firstOfType("terraform")
	if blk == nil {
		return nil
	}
	return blk.(*Terraform)
}

// AddTerraform appends a `terraform` block.
func (f *File) AddTerraform() *Terraform {
	if f == nil {
		return nil
	}
	return f.body.appendBlock("terraform").(*Terraform)
}

// Locals returns every `locals` block; Terraform allows more than one.
func (f *File) Locals() []*Locals {
	if f == nil {
		return nil
	}
	var out []*Locals
	for _, blk := range f.body.blocksOfType("locals") {
		out = append(out, blk.(*Locals))
	}
	return out
}

// AddLocals appends a `locals` block.
func (f *File) AddLocals() *Locals {
	if f == nil {
		return nil
	}
	return f.body.appendBlock("locals").(*Locals)
}

// Moved returns every `moved` block.
func (f *File) Moved() []*Moved {
	if f == nil {
		return nil
	}
	var out []*Moved
	for _, blk := range f.body.blocksOfType("moved") {
		out = append(out, blk.(*Moved))
	}
	return out
}

// AddMoved appends a `moved` block.
func (f *File) AddMoved() *Moved {
	if f == nil {
		return nil
	}
	return f.body.appendBlock("moved").(*Moved)
}

// Removed returns every `removed` block.
func (f *File) Removed() []*Removed {
	if f == nil {
		return nil
	}
	var out []*Removed
	for _, blk := range f.body.blocksOfType("removed") {
		out = append(out, blk.(*Removed))
	}
	return out
}

// AddRemoved appends a `removed` block.
func (f *File) AddRemoved() *Removed {
	if f == nil {
		return nil
	}
	return f.body.appendBlock("removed").(*Removed)
}

// Imports returns every `import` block.
func (f *File) Imports() []*Import {
	if f == nil {
		return nil
	}
	var out []*Import
	for _, blk := range f.body.blocksOfType("import") {
		out = append(out, blk.(*Import))
	}
	return out
}

// AddImport appends an `import` block.
func (f *File) AddImport() *Import {
	if f == nil {
		return nil
	}
	return f.body.appendBlock("import").(*Import)
}

// Comments returns the file's top-level comments, in source order. A
// comment inside a block belongs to that block.
func (f *File) Comments() []*Comment {
	if f == nil {
		return nil
	}
	return f.body.Comments()
}

// Items returns everything at the top level in source order — blocks and
// the comments between them.
func (f *File) Items() []Item {
	if f == nil {
		return nil
	}
	return f.body.Items()
}

// Blocks returns every top-level block, whatever its type.
func (f *File) Blocks() []Block {
	if f == nil {
		return nil
	}
	return f.body.Blocks()
}

// Remove drops a top-level item. Passing the result of an accessor that
// found nothing is a no-op rather than a panic, so
// f.Remove(f.Resource("x", "y")) is safe whether or not it is there.
func (f *File) Remove(target Item) bool {
	if f == nil {
		return false
	}
	return f.body.Remove(target)
}
