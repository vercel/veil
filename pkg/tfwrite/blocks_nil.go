package tfwrite

import "strings"

// ---- nil-safe surface ---------------------------------------------------
//
// Every lookup returns nil when it finds nothing, so a chain like
//
//	f.Resource("aws_s3_bucket", "logs").Body().Attribute("acl").Delete()
//
// has to survive a miss at any link. These methods are written out per
// type rather than promoted from the embedded block: a promoted method
// takes the address of a field, which dereferences the nil receiver
// before the body can test for it.

func (r *Resource) BlockType() string {
	if r == nil {
		return ""
	}
	return r.block.blockType
}

func (r *Resource) Labels() []string {
	if r == nil {
		return nil
	}
	return r.block.Labels()
}

// Body returns the block's contents, or nil when the block is nil. The
// Body methods are nil-safe in turn, so the chain keeps going.
func (r *Resource) Body() *Body {
	if r == nil {
		return nil
	}
	return r.block.body
}

func (r *Resource) span() srcSpan {
	if r == nil {
		return srcSpan{}
	}
	return r.block.sp
}

func (r *Resource) print(src []byte, indent int, b *strings.Builder) {
	if r == nil {
		return
	}
	r.block.print(src, indent, b)
}

func (d *DataSource) BlockType() string {
	if d == nil {
		return ""
	}
	return d.block.blockType
}

func (d *DataSource) Labels() []string {
	if d == nil {
		return nil
	}
	return d.block.Labels()
}

// Body returns the block's contents, or nil when the block is nil. The
// Body methods are nil-safe in turn, so the chain keeps going.
func (d *DataSource) Body() *Body {
	if d == nil {
		return nil
	}
	return d.block.body
}

func (d *DataSource) span() srcSpan {
	if d == nil {
		return srcSpan{}
	}
	return d.block.sp
}

func (d *DataSource) print(src []byte, indent int, b *strings.Builder) {
	if d == nil {
		return
	}
	d.block.print(src, indent, b)
}

func (e *Ephemeral) BlockType() string {
	if e == nil {
		return ""
	}
	return e.block.blockType
}

func (e *Ephemeral) Labels() []string {
	if e == nil {
		return nil
	}
	return e.block.Labels()
}

// Body returns the block's contents, or nil when the block is nil. The
// Body methods are nil-safe in turn, so the chain keeps going.
func (e *Ephemeral) Body() *Body {
	if e == nil {
		return nil
	}
	return e.block.body
}

func (e *Ephemeral) span() srcSpan {
	if e == nil {
		return srcSpan{}
	}
	return e.block.sp
}

func (e *Ephemeral) print(src []byte, indent int, b *strings.Builder) {
	if e == nil {
		return
	}
	e.block.print(src, indent, b)
}

func (a *Action) BlockType() string {
	if a == nil {
		return ""
	}
	return a.block.blockType
}

func (a *Action) Labels() []string {
	if a == nil {
		return nil
	}
	return a.block.Labels()
}

// Body returns the block's contents, or nil when the block is nil. The
// Body methods are nil-safe in turn, so the chain keeps going.
func (a *Action) Body() *Body {
	if a == nil {
		return nil
	}
	return a.block.body
}

func (a *Action) span() srcSpan {
	if a == nil {
		return srcSpan{}
	}
	return a.block.sp
}

func (a *Action) print(src []byte, indent int, b *strings.Builder) {
	if a == nil {
		return
	}
	a.block.print(src, indent, b)
}

func (p *Provider) BlockType() string {
	if p == nil {
		return ""
	}
	return p.block.blockType
}

func (p *Provider) Labels() []string {
	if p == nil {
		return nil
	}
	return p.block.Labels()
}

// Body returns the block's contents, or nil when the block is nil. The
// Body methods are nil-safe in turn, so the chain keeps going.
func (p *Provider) Body() *Body {
	if p == nil {
		return nil
	}
	return p.block.body
}

func (p *Provider) span() srcSpan {
	if p == nil {
		return srcSpan{}
	}
	return p.block.sp
}

func (p *Provider) print(src []byte, indent int, b *strings.Builder) {
	if p == nil {
		return
	}
	p.block.print(src, indent, b)
}

func (v *Variable) BlockType() string {
	if v == nil {
		return ""
	}
	return v.block.blockType
}

func (v *Variable) Labels() []string {
	if v == nil {
		return nil
	}
	return v.block.Labels()
}

// Body returns the block's contents, or nil when the block is nil. The
// Body methods are nil-safe in turn, so the chain keeps going.
func (v *Variable) Body() *Body {
	if v == nil {
		return nil
	}
	return v.block.body
}

func (v *Variable) span() srcSpan {
	if v == nil {
		return srcSpan{}
	}
	return v.block.sp
}

func (v *Variable) print(src []byte, indent int, b *strings.Builder) {
	if v == nil {
		return
	}
	v.block.print(src, indent, b)
}

func (o *Output) BlockType() string {
	if o == nil {
		return ""
	}
	return o.block.blockType
}

func (o *Output) Labels() []string {
	if o == nil {
		return nil
	}
	return o.block.Labels()
}

// Body returns the block's contents, or nil when the block is nil. The
// Body methods are nil-safe in turn, so the chain keeps going.
func (o *Output) Body() *Body {
	if o == nil {
		return nil
	}
	return o.block.body
}

func (o *Output) span() srcSpan {
	if o == nil {
		return srcSpan{}
	}
	return o.block.sp
}

func (o *Output) print(src []byte, indent int, b *strings.Builder) {
	if o == nil {
		return
	}
	o.block.print(src, indent, b)
}

func (m *Module) BlockType() string {
	if m == nil {
		return ""
	}
	return m.block.blockType
}

func (m *Module) Labels() []string {
	if m == nil {
		return nil
	}
	return m.block.Labels()
}

// Body returns the block's contents, or nil when the block is nil. The
// Body methods are nil-safe in turn, so the chain keeps going.
func (m *Module) Body() *Body {
	if m == nil {
		return nil
	}
	return m.block.body
}

func (m *Module) span() srcSpan {
	if m == nil {
		return srcSpan{}
	}
	return m.block.sp
}

func (m *Module) print(src []byte, indent int, b *strings.Builder) {
	if m == nil {
		return
	}
	m.block.print(src, indent, b)
}

func (c *Check) BlockType() string {
	if c == nil {
		return ""
	}
	return c.block.blockType
}

func (c *Check) Labels() []string {
	if c == nil {
		return nil
	}
	return c.block.Labels()
}

// Body returns the block's contents, or nil when the block is nil. The
// Body methods are nil-safe in turn, so the chain keeps going.
func (c *Check) Body() *Body {
	if c == nil {
		return nil
	}
	return c.block.body
}

func (c *Check) span() srcSpan {
	if c == nil {
		return srcSpan{}
	}
	return c.block.sp
}

func (c *Check) print(src []byte, indent int, b *strings.Builder) {
	if c == nil {
		return
	}
	c.block.print(src, indent, b)
}

func (t *Terraform) BlockType() string {
	if t == nil {
		return ""
	}
	return t.block.blockType
}

func (t *Terraform) Labels() []string {
	if t == nil {
		return nil
	}
	return t.block.Labels()
}

// Body returns the block's contents, or nil when the block is nil. The
// Body methods are nil-safe in turn, so the chain keeps going.
func (t *Terraform) Body() *Body {
	if t == nil {
		return nil
	}
	return t.block.body
}

func (t *Terraform) span() srcSpan {
	if t == nil {
		return srcSpan{}
	}
	return t.block.sp
}

func (t *Terraform) print(src []byte, indent int, b *strings.Builder) {
	if t == nil {
		return
	}
	t.block.print(src, indent, b)
}

func (l *Locals) BlockType() string {
	if l == nil {
		return ""
	}
	return l.block.blockType
}

func (l *Locals) Labels() []string {
	if l == nil {
		return nil
	}
	return l.block.Labels()
}

// Body returns the block's contents, or nil when the block is nil. The
// Body methods are nil-safe in turn, so the chain keeps going.
func (l *Locals) Body() *Body {
	if l == nil {
		return nil
	}
	return l.block.body
}

func (l *Locals) span() srcSpan {
	if l == nil {
		return srcSpan{}
	}
	return l.block.sp
}

func (l *Locals) print(src []byte, indent int, b *strings.Builder) {
	if l == nil {
		return
	}
	l.block.print(src, indent, b)
}

func (mv *Moved) BlockType() string {
	if mv == nil {
		return ""
	}
	return mv.block.blockType
}

func (mv *Moved) Labels() []string {
	if mv == nil {
		return nil
	}
	return mv.block.Labels()
}

// Body returns the block's contents, or nil when the block is nil. The
// Body methods are nil-safe in turn, so the chain keeps going.
func (mv *Moved) Body() *Body {
	if mv == nil {
		return nil
	}
	return mv.block.body
}

func (mv *Moved) span() srcSpan {
	if mv == nil {
		return srcSpan{}
	}
	return mv.block.sp
}

func (mv *Moved) print(src []byte, indent int, b *strings.Builder) {
	if mv == nil {
		return
	}
	mv.block.print(src, indent, b)
}

func (rm *Removed) BlockType() string {
	if rm == nil {
		return ""
	}
	return rm.block.blockType
}

func (rm *Removed) Labels() []string {
	if rm == nil {
		return nil
	}
	return rm.block.Labels()
}

// Body returns the block's contents, or nil when the block is nil. The
// Body methods are nil-safe in turn, so the chain keeps going.
func (rm *Removed) Body() *Body {
	if rm == nil {
		return nil
	}
	return rm.block.body
}

func (rm *Removed) span() srcSpan {
	if rm == nil {
		return srcSpan{}
	}
	return rm.block.sp
}

func (rm *Removed) print(src []byte, indent int, b *strings.Builder) {
	if rm == nil {
		return
	}
	rm.block.print(src, indent, b)
}

func (i *Import) BlockType() string {
	if i == nil {
		return ""
	}
	return i.block.blockType
}

func (i *Import) Labels() []string {
	if i == nil {
		return nil
	}
	return i.block.Labels()
}

// Body returns the block's contents, or nil when the block is nil. The
// Body methods are nil-safe in turn, so the chain keeps going.
func (i *Import) Body() *Body {
	if i == nil {
		return nil
	}
	return i.block.body
}

func (i *Import) span() srcSpan {
	if i == nil {
		return srcSpan{}
	}
	return i.block.sp
}

func (i *Import) print(src []byte, indent int, b *strings.Builder) {
	if i == nil {
		return
	}
	i.block.print(src, indent, b)
}

func (g *Generic) BlockType() string {
	if g == nil {
		return ""
	}
	return g.block.blockType
}

func (g *Generic) Labels() []string {
	if g == nil {
		return nil
	}
	return g.block.Labels()
}

// Body returns the block's contents, or nil when the block is nil. The
// Body methods are nil-safe in turn, so the chain keeps going.
func (g *Generic) Body() *Body {
	if g == nil {
		return nil
	}
	return g.block.body
}

func (g *Generic) span() srcSpan {
	if g == nil {
		return srcSpan{}
	}
	return g.block.sp
}

func (g *Generic) print(src []byte, indent int, b *strings.Builder) {
	if g == nil {
		return
	}
	g.block.print(src, indent, b)
}
