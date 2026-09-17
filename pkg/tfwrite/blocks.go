package tfwrite

// The concrete block types. Each carries exactly the labels Terraform's
// own schema gives it, named as Terraform names them, so the API cannot
// express a block shape Terraform would reject.
//
//	resource "aws_s3_bucket" "logs"   type + name
//	provider "aws"                    name
//	backend  "s3"                     type
//	locals                            none
//
// Top-level blocks are here; nested ones (lifecycle, connection,
// validation, provisioner) are reached through a parent's Body and are
// generic, since their shapes are the parent's business.

// ---- two labels: type + name -------------------------------------------

// Resource is `resource "TYPE" "NAME"`.
type Resource struct{ block }

func (r *Resource) ResourceType() string     { return r.label(0) }
func (r *Resource) SetResourceType(t string) { r.setLabel(0, t) }
func (r *Resource) Name() string             { return r.label(1) }
func (r *Resource) SetName(n string)         { r.setLabel(1, n) }

// DataSource is `data "TYPE" "NAME"`.
type DataSource struct{ block }

func (d *DataSource) DataType() string     { return d.label(0) }
func (d *DataSource) SetDataType(t string) { d.setLabel(0, t) }
func (d *DataSource) Name() string         { return d.label(1) }
func (d *DataSource) SetName(n string)     { d.setLabel(1, n) }

// Ephemeral is `ephemeral "TYPE" "NAME"`.
type Ephemeral struct{ block }

func (e *Ephemeral) EphemeralType() string     { return e.label(0) }
func (e *Ephemeral) SetEphemeralType(t string) { e.setLabel(0, t) }
func (e *Ephemeral) Name() string              { return e.label(1) }
func (e *Ephemeral) SetName(n string)          { e.setLabel(1, n) }

// Action is `action "TYPE" "NAME"`.
type Action struct{ block }

func (a *Action) ActionType() string     { return a.label(0) }
func (a *Action) SetActionType(t string) { a.setLabel(0, t) }
func (a *Action) Name() string           { return a.label(1) }
func (a *Action) SetName(n string)       { a.setLabel(1, n) }

// ---- one label: a name -------------------------------------------------

// Provider is `provider "NAME"`. The label is the provider's local name;
// several blocks may share it, distinguished by an `alias` attribute.
type Provider struct{ block }

func (p *Provider) Name() string     { return p.label(0) }
func (p *Provider) SetName(n string) { p.setLabel(0, n) }

// Alias reads the `alias` attribute, which is how a configuration tells
// two blocks for the same provider apart. Empty when unset.
func (p *Provider) Alias() string {
	attr := p.body.Attribute("alias")
	if attr == nil {
		return ""
	}
	return unquote(attr.Expr)
}

// Variable is `variable "NAME"`.
type Variable struct{ block }

func (v *Variable) Name() string     { return v.label(0) }
func (v *Variable) SetName(n string) { v.setLabel(0, n) }

// Output is `output "NAME"`.
type Output struct{ block }

func (o *Output) Name() string     { return o.label(0) }
func (o *Output) SetName(n string) { o.setLabel(0, n) }

// Module is `module "NAME"`.
type Module struct{ block }

func (m *Module) Name() string     { return m.label(0) }
func (m *Module) SetName(n string) { m.setLabel(0, n) }

// Source reads the module's `source`, the one attribute every module
// must have. Empty when unset.
func (m *Module) Source() string {
	attr := m.body.Attribute("source")
	if attr == nil {
		return ""
	}
	return unquote(attr.Expr)
}

// Check is `check "NAME"`.
type Check struct{ block }

func (c *Check) Name() string     { return c.label(0) }
func (c *Check) SetName(n string) { c.setLabel(0, n) }

// ---- no labels ---------------------------------------------------------

// Terraform is the `terraform` settings block.
type Terraform struct{ block }

// Locals is a `locals` block. Terraform allows more than one per file.
type Locals struct{ block }

// Moved is a `moved` block.
type Moved struct{ block }

// Removed is a `removed` block.
type Removed struct{ block }

// Import is an `import` block.
type Import struct{ block }

// Generic is a block this package does not model — a nested one, or a
// type Terraform adds after this was written. It is still readable and
// editable, just without named label accessors.
type Generic struct{ block }

// SetLabels replaces a Generic block's labels. Only Generic has this:
// for a modelled block the named setters say which label is which, and
// a positional list is how you produce a shape Terraform rejects.
func (g *Generic) SetLabels(labels []string) {
	g.labels = append([]string(nil), labels...)
	g.labelsChanged = true
}

// newBlock builds the concrete type for a block keyword. Anything not
// listed — nested blocks, future additions — becomes Generic rather
// than an error, so an unfamiliar file still parses and round-trips.
func newBlock(blockType string, labels []string, body *Body, sp srcSpan) Block {
	b := block{blockType: blockType, labels: labels, body: body, sp: sp}
	switch blockType {
	case "resource":
		return &Resource{b}
	case "data":
		return &DataSource{b}
	case "ephemeral":
		return &Ephemeral{b}
	case "action":
		return &Action{b}
	case "provider":
		return &Provider{b}
	case "variable":
		return &Variable{b}
	case "output":
		return &Output{b}
	case "module":
		return &Module{b}
	case "check":
		return &Check{b}
	case "terraform":
		return &Terraform{b}
	case "locals":
		return &Locals{b}
	case "moved":
		return &Moved{b}
	case "removed":
		return &Removed{b}
	case "import":
		return &Import{b}
	default:
		return &Generic{b}
	}
}

// unquote strips surrounding double quotes from an expression that is a
// plain string literal, leaving anything else alone — a reference has no
// unquoted form worth reporting.
func unquote(expr string) string {
	if len(expr) >= 2 && expr[0] == '"' && expr[len(expr)-1] == '"' {
		return expr[1 : len(expr)-1]
	}
	return expr
}

// blockBase reaches the shared state behind any concrete block, for the
// printer and the serializer — the parts that treat every block alike.
func blockBase(b Block) *block {
	switch v := b.(type) {
	case *Resource:
		return &v.block
	case *DataSource:
		return &v.block
	case *Ephemeral:
		return &v.block
	case *Action:
		return &v.block
	case *Provider:
		return &v.block
	case *Variable:
		return &v.block
	case *Output:
		return &v.block
	case *Module:
		return &v.block
	case *Check:
		return &v.block
	case *Terraform:
		return &v.block
	case *Locals:
		return &v.block
	case *Moved:
		return &v.block
	case *Removed:
		return &v.block
	case *Import:
		return &v.block
	case *Generic:
		return &v.block
	default:
		return nil
	}
}
