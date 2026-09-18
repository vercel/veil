package tfwrite

// ---- the body's methods, on the block ------------------------------------
//
// A block is the thing you looked up, so it answers about its own
// contents directly:
//
//	f.Resource("aws_s3_bucket", "logs").SetAttribute("acl", `"private"`)
//
// rather than routing through Body() every time. Body() is still there
// for holding onto one, which is what the printer and the JSON layer do.
//
// Written out per type for the same reason as the rest: a method
// promoted from the embedded block takes the address of a field and
// dereferences a nil receiver before its body can test for one.

func (r *Resource) Attribute(name string) *Attribute {
	if r == nil {
		return nil
	}
	return r.block.body.Attribute(name)
}
func (r *Resource) Attributes() []*Attribute {
	if r == nil {
		return nil
	}
	return r.block.body.Attributes()
}
func (r *Resource) SetAttribute(name, expr string) *Attribute {
	if r == nil {
		return nil
	}
	return r.block.body.SetAttribute(name, expr)
}
func (r *Resource) RemoveAttribute(name string) bool {
	if r == nil {
		return false
	}
	return r.block.body.RemoveAttribute(name)
}
func (r *Resource) Blocks() []Block {
	if r == nil {
		return nil
	}
	return r.block.body.Blocks()
}
func (r *Resource) Block(blockType string, labels ...string) Block {
	if r == nil {
		return nil
	}
	return r.block.body.Block(blockType, labels...)
}
func (r *Resource) AddBlock(blockType string, labels ...string) *Generic {
	if r == nil {
		return nil
	}
	return r.block.body.AddBlock(blockType, labels...)
}
func (r *Resource) Comments() []*Comment {
	if r == nil {
		return nil
	}
	return r.block.body.Comments()
}
func (r *Resource) AppendComment(text string) *Comment {
	if r == nil {
		return nil
	}
	return r.block.body.AppendComment(text)
}
func (r *Resource) Items() []Item {
	if r == nil {
		return nil
	}
	return r.block.body.Items()
}
func (r *Resource) Remove(target Item) bool {
	if r == nil {
		return false
	}
	return r.block.body.Remove(target)
}
func (d *DataSource) Attribute(name string) *Attribute {
	if d == nil {
		return nil
	}
	return d.block.body.Attribute(name)
}
func (d *DataSource) Attributes() []*Attribute {
	if d == nil {
		return nil
	}
	return d.block.body.Attributes()
}
func (d *DataSource) SetAttribute(name, expr string) *Attribute {
	if d == nil {
		return nil
	}
	return d.block.body.SetAttribute(name, expr)
}
func (d *DataSource) RemoveAttribute(name string) bool {
	if d == nil {
		return false
	}
	return d.block.body.RemoveAttribute(name)
}
func (d *DataSource) Blocks() []Block {
	if d == nil {
		return nil
	}
	return d.block.body.Blocks()
}
func (d *DataSource) Block(blockType string, labels ...string) Block {
	if d == nil {
		return nil
	}
	return d.block.body.Block(blockType, labels...)
}
func (d *DataSource) AddBlock(blockType string, labels ...string) *Generic {
	if d == nil {
		return nil
	}
	return d.block.body.AddBlock(blockType, labels...)
}
func (d *DataSource) Comments() []*Comment {
	if d == nil {
		return nil
	}
	return d.block.body.Comments()
}
func (d *DataSource) AppendComment(text string) *Comment {
	if d == nil {
		return nil
	}
	return d.block.body.AppendComment(text)
}
func (d *DataSource) Items() []Item {
	if d == nil {
		return nil
	}
	return d.block.body.Items()
}
func (d *DataSource) Remove(target Item) bool {
	if d == nil {
		return false
	}
	return d.block.body.Remove(target)
}
func (e *Ephemeral) Attribute(name string) *Attribute {
	if e == nil {
		return nil
	}
	return e.block.body.Attribute(name)
}
func (e *Ephemeral) Attributes() []*Attribute {
	if e == nil {
		return nil
	}
	return e.block.body.Attributes()
}
func (e *Ephemeral) SetAttribute(name, expr string) *Attribute {
	if e == nil {
		return nil
	}
	return e.block.body.SetAttribute(name, expr)
}
func (e *Ephemeral) RemoveAttribute(name string) bool {
	if e == nil {
		return false
	}
	return e.block.body.RemoveAttribute(name)
}
func (e *Ephemeral) Blocks() []Block {
	if e == nil {
		return nil
	}
	return e.block.body.Blocks()
}
func (e *Ephemeral) Block(blockType string, labels ...string) Block {
	if e == nil {
		return nil
	}
	return e.block.body.Block(blockType, labels...)
}
func (e *Ephemeral) AddBlock(blockType string, labels ...string) *Generic {
	if e == nil {
		return nil
	}
	return e.block.body.AddBlock(blockType, labels...)
}
func (e *Ephemeral) Comments() []*Comment {
	if e == nil {
		return nil
	}
	return e.block.body.Comments()
}
func (e *Ephemeral) AppendComment(text string) *Comment {
	if e == nil {
		return nil
	}
	return e.block.body.AppendComment(text)
}
func (e *Ephemeral) Items() []Item {
	if e == nil {
		return nil
	}
	return e.block.body.Items()
}
func (e *Ephemeral) Remove(target Item) bool {
	if e == nil {
		return false
	}
	return e.block.body.Remove(target)
}
func (a *Action) Attribute(name string) *Attribute {
	if a == nil {
		return nil
	}
	return a.block.body.Attribute(name)
}
func (a *Action) Attributes() []*Attribute {
	if a == nil {
		return nil
	}
	return a.block.body.Attributes()
}
func (a *Action) SetAttribute(name, expr string) *Attribute {
	if a == nil {
		return nil
	}
	return a.block.body.SetAttribute(name, expr)
}
func (a *Action) RemoveAttribute(name string) bool {
	if a == nil {
		return false
	}
	return a.block.body.RemoveAttribute(name)
}
func (a *Action) Blocks() []Block {
	if a == nil {
		return nil
	}
	return a.block.body.Blocks()
}
func (a *Action) Block(blockType string, labels ...string) Block {
	if a == nil {
		return nil
	}
	return a.block.body.Block(blockType, labels...)
}
func (a *Action) AddBlock(blockType string, labels ...string) *Generic {
	if a == nil {
		return nil
	}
	return a.block.body.AddBlock(blockType, labels...)
}
func (a *Action) Comments() []*Comment {
	if a == nil {
		return nil
	}
	return a.block.body.Comments()
}
func (a *Action) AppendComment(text string) *Comment {
	if a == nil {
		return nil
	}
	return a.block.body.AppendComment(text)
}
func (a *Action) Items() []Item {
	if a == nil {
		return nil
	}
	return a.block.body.Items()
}
func (a *Action) Remove(target Item) bool {
	if a == nil {
		return false
	}
	return a.block.body.Remove(target)
}
func (p *Provider) Attribute(name string) *Attribute {
	if p == nil {
		return nil
	}
	return p.block.body.Attribute(name)
}
func (p *Provider) Attributes() []*Attribute {
	if p == nil {
		return nil
	}
	return p.block.body.Attributes()
}
func (p *Provider) SetAttribute(name, expr string) *Attribute {
	if p == nil {
		return nil
	}
	return p.block.body.SetAttribute(name, expr)
}
func (p *Provider) RemoveAttribute(name string) bool {
	if p == nil {
		return false
	}
	return p.block.body.RemoveAttribute(name)
}
func (p *Provider) Blocks() []Block {
	if p == nil {
		return nil
	}
	return p.block.body.Blocks()
}
func (p *Provider) Block(blockType string, labels ...string) Block {
	if p == nil {
		return nil
	}
	return p.block.body.Block(blockType, labels...)
}
func (p *Provider) AddBlock(blockType string, labels ...string) *Generic {
	if p == nil {
		return nil
	}
	return p.block.body.AddBlock(blockType, labels...)
}
func (p *Provider) Comments() []*Comment {
	if p == nil {
		return nil
	}
	return p.block.body.Comments()
}
func (p *Provider) AppendComment(text string) *Comment {
	if p == nil {
		return nil
	}
	return p.block.body.AppendComment(text)
}
func (p *Provider) Items() []Item {
	if p == nil {
		return nil
	}
	return p.block.body.Items()
}
func (p *Provider) Remove(target Item) bool {
	if p == nil {
		return false
	}
	return p.block.body.Remove(target)
}
func (v *Variable) Attribute(name string) *Attribute {
	if v == nil {
		return nil
	}
	return v.block.body.Attribute(name)
}
func (v *Variable) Attributes() []*Attribute {
	if v == nil {
		return nil
	}
	return v.block.body.Attributes()
}
func (v *Variable) SetAttribute(name, expr string) *Attribute {
	if v == nil {
		return nil
	}
	return v.block.body.SetAttribute(name, expr)
}
func (v *Variable) RemoveAttribute(name string) bool {
	if v == nil {
		return false
	}
	return v.block.body.RemoveAttribute(name)
}
func (v *Variable) Blocks() []Block {
	if v == nil {
		return nil
	}
	return v.block.body.Blocks()
}
func (v *Variable) Block(blockType string, labels ...string) Block {
	if v == nil {
		return nil
	}
	return v.block.body.Block(blockType, labels...)
}
func (v *Variable) AddBlock(blockType string, labels ...string) *Generic {
	if v == nil {
		return nil
	}
	return v.block.body.AddBlock(blockType, labels...)
}
func (v *Variable) Comments() []*Comment {
	if v == nil {
		return nil
	}
	return v.block.body.Comments()
}
func (v *Variable) AppendComment(text string) *Comment {
	if v == nil {
		return nil
	}
	return v.block.body.AppendComment(text)
}
func (v *Variable) Items() []Item {
	if v == nil {
		return nil
	}
	return v.block.body.Items()
}
func (v *Variable) Remove(target Item) bool {
	if v == nil {
		return false
	}
	return v.block.body.Remove(target)
}
func (o *Output) Attribute(name string) *Attribute {
	if o == nil {
		return nil
	}
	return o.block.body.Attribute(name)
}
func (o *Output) Attributes() []*Attribute {
	if o == nil {
		return nil
	}
	return o.block.body.Attributes()
}
func (o *Output) SetAttribute(name, expr string) *Attribute {
	if o == nil {
		return nil
	}
	return o.block.body.SetAttribute(name, expr)
}
func (o *Output) RemoveAttribute(name string) bool {
	if o == nil {
		return false
	}
	return o.block.body.RemoveAttribute(name)
}
func (o *Output) Blocks() []Block {
	if o == nil {
		return nil
	}
	return o.block.body.Blocks()
}
func (o *Output) Block(blockType string, labels ...string) Block {
	if o == nil {
		return nil
	}
	return o.block.body.Block(blockType, labels...)
}
func (o *Output) AddBlock(blockType string, labels ...string) *Generic {
	if o == nil {
		return nil
	}
	return o.block.body.AddBlock(blockType, labels...)
}
func (o *Output) Comments() []*Comment {
	if o == nil {
		return nil
	}
	return o.block.body.Comments()
}
func (o *Output) AppendComment(text string) *Comment {
	if o == nil {
		return nil
	}
	return o.block.body.AppendComment(text)
}
func (o *Output) Items() []Item {
	if o == nil {
		return nil
	}
	return o.block.body.Items()
}
func (o *Output) Remove(target Item) bool {
	if o == nil {
		return false
	}
	return o.block.body.Remove(target)
}
func (m *Module) Attribute(name string) *Attribute {
	if m == nil {
		return nil
	}
	return m.block.body.Attribute(name)
}
func (m *Module) Attributes() []*Attribute {
	if m == nil {
		return nil
	}
	return m.block.body.Attributes()
}
func (m *Module) SetAttribute(name, expr string) *Attribute {
	if m == nil {
		return nil
	}
	return m.block.body.SetAttribute(name, expr)
}
func (m *Module) RemoveAttribute(name string) bool {
	if m == nil {
		return false
	}
	return m.block.body.RemoveAttribute(name)
}
func (m *Module) Blocks() []Block {
	if m == nil {
		return nil
	}
	return m.block.body.Blocks()
}
func (m *Module) Block(blockType string, labels ...string) Block {
	if m == nil {
		return nil
	}
	return m.block.body.Block(blockType, labels...)
}
func (m *Module) AddBlock(blockType string, labels ...string) *Generic {
	if m == nil {
		return nil
	}
	return m.block.body.AddBlock(blockType, labels...)
}
func (m *Module) Comments() []*Comment {
	if m == nil {
		return nil
	}
	return m.block.body.Comments()
}
func (m *Module) AppendComment(text string) *Comment {
	if m == nil {
		return nil
	}
	return m.block.body.AppendComment(text)
}
func (m *Module) Items() []Item {
	if m == nil {
		return nil
	}
	return m.block.body.Items()
}
func (m *Module) Remove(target Item) bool {
	if m == nil {
		return false
	}
	return m.block.body.Remove(target)
}
func (c *Check) Attribute(name string) *Attribute {
	if c == nil {
		return nil
	}
	return c.block.body.Attribute(name)
}
func (c *Check) Attributes() []*Attribute {
	if c == nil {
		return nil
	}
	return c.block.body.Attributes()
}
func (c *Check) SetAttribute(name, expr string) *Attribute {
	if c == nil {
		return nil
	}
	return c.block.body.SetAttribute(name, expr)
}
func (c *Check) RemoveAttribute(name string) bool {
	if c == nil {
		return false
	}
	return c.block.body.RemoveAttribute(name)
}
func (c *Check) Blocks() []Block {
	if c == nil {
		return nil
	}
	return c.block.body.Blocks()
}
func (c *Check) Block(blockType string, labels ...string) Block {
	if c == nil {
		return nil
	}
	return c.block.body.Block(blockType, labels...)
}
func (c *Check) AddBlock(blockType string, labels ...string) *Generic {
	if c == nil {
		return nil
	}
	return c.block.body.AddBlock(blockType, labels...)
}
func (c *Check) Comments() []*Comment {
	if c == nil {
		return nil
	}
	return c.block.body.Comments()
}
func (c *Check) AppendComment(text string) *Comment {
	if c == nil {
		return nil
	}
	return c.block.body.AppendComment(text)
}
func (c *Check) Items() []Item {
	if c == nil {
		return nil
	}
	return c.block.body.Items()
}
func (c *Check) Remove(target Item) bool {
	if c == nil {
		return false
	}
	return c.block.body.Remove(target)
}
func (t *Terraform) Attribute(name string) *Attribute {
	if t == nil {
		return nil
	}
	return t.block.body.Attribute(name)
}
func (t *Terraform) Attributes() []*Attribute {
	if t == nil {
		return nil
	}
	return t.block.body.Attributes()
}
func (t *Terraform) SetAttribute(name, expr string) *Attribute {
	if t == nil {
		return nil
	}
	return t.block.body.SetAttribute(name, expr)
}
func (t *Terraform) RemoveAttribute(name string) bool {
	if t == nil {
		return false
	}
	return t.block.body.RemoveAttribute(name)
}
func (t *Terraform) Blocks() []Block {
	if t == nil {
		return nil
	}
	return t.block.body.Blocks()
}
func (t *Terraform) Block(blockType string, labels ...string) Block {
	if t == nil {
		return nil
	}
	return t.block.body.Block(blockType, labels...)
}
func (t *Terraform) AddBlock(blockType string, labels ...string) *Generic {
	if t == nil {
		return nil
	}
	return t.block.body.AddBlock(blockType, labels...)
}
func (t *Terraform) Comments() []*Comment {
	if t == nil {
		return nil
	}
	return t.block.body.Comments()
}
func (t *Terraform) AppendComment(text string) *Comment {
	if t == nil {
		return nil
	}
	return t.block.body.AppendComment(text)
}
func (t *Terraform) Items() []Item {
	if t == nil {
		return nil
	}
	return t.block.body.Items()
}
func (t *Terraform) Remove(target Item) bool {
	if t == nil {
		return false
	}
	return t.block.body.Remove(target)
}
func (l *Locals) Attribute(name string) *Attribute {
	if l == nil {
		return nil
	}
	return l.block.body.Attribute(name)
}
func (l *Locals) Attributes() []*Attribute {
	if l == nil {
		return nil
	}
	return l.block.body.Attributes()
}
func (l *Locals) SetAttribute(name, expr string) *Attribute {
	if l == nil {
		return nil
	}
	return l.block.body.SetAttribute(name, expr)
}
func (l *Locals) RemoveAttribute(name string) bool {
	if l == nil {
		return false
	}
	return l.block.body.RemoveAttribute(name)
}
func (l *Locals) Blocks() []Block {
	if l == nil {
		return nil
	}
	return l.block.body.Blocks()
}
func (l *Locals) Block(blockType string, labels ...string) Block {
	if l == nil {
		return nil
	}
	return l.block.body.Block(blockType, labels...)
}
func (l *Locals) AddBlock(blockType string, labels ...string) *Generic {
	if l == nil {
		return nil
	}
	return l.block.body.AddBlock(blockType, labels...)
}
func (l *Locals) Comments() []*Comment {
	if l == nil {
		return nil
	}
	return l.block.body.Comments()
}
func (l *Locals) AppendComment(text string) *Comment {
	if l == nil {
		return nil
	}
	return l.block.body.AppendComment(text)
}
func (l *Locals) Items() []Item {
	if l == nil {
		return nil
	}
	return l.block.body.Items()
}
func (l *Locals) Remove(target Item) bool {
	if l == nil {
		return false
	}
	return l.block.body.Remove(target)
}
func (mv *Moved) Attribute(name string) *Attribute {
	if mv == nil {
		return nil
	}
	return mv.block.body.Attribute(name)
}
func (mv *Moved) Attributes() []*Attribute {
	if mv == nil {
		return nil
	}
	return mv.block.body.Attributes()
}
func (mv *Moved) SetAttribute(name, expr string) *Attribute {
	if mv == nil {
		return nil
	}
	return mv.block.body.SetAttribute(name, expr)
}
func (mv *Moved) RemoveAttribute(name string) bool {
	if mv == nil {
		return false
	}
	return mv.block.body.RemoveAttribute(name)
}
func (mv *Moved) Blocks() []Block {
	if mv == nil {
		return nil
	}
	return mv.block.body.Blocks()
}
func (mv *Moved) Block(blockType string, labels ...string) Block {
	if mv == nil {
		return nil
	}
	return mv.block.body.Block(blockType, labels...)
}
func (mv *Moved) AddBlock(blockType string, labels ...string) *Generic {
	if mv == nil {
		return nil
	}
	return mv.block.body.AddBlock(blockType, labels...)
}
func (mv *Moved) Comments() []*Comment {
	if mv == nil {
		return nil
	}
	return mv.block.body.Comments()
}
func (mv *Moved) AppendComment(text string) *Comment {
	if mv == nil {
		return nil
	}
	return mv.block.body.AppendComment(text)
}
func (mv *Moved) Items() []Item {
	if mv == nil {
		return nil
	}
	return mv.block.body.Items()
}
func (mv *Moved) Remove(target Item) bool {
	if mv == nil {
		return false
	}
	return mv.block.body.Remove(target)
}
func (rm *Removed) Attribute(name string) *Attribute {
	if rm == nil {
		return nil
	}
	return rm.block.body.Attribute(name)
}
func (rm *Removed) Attributes() []*Attribute {
	if rm == nil {
		return nil
	}
	return rm.block.body.Attributes()
}
func (rm *Removed) SetAttribute(name, expr string) *Attribute {
	if rm == nil {
		return nil
	}
	return rm.block.body.SetAttribute(name, expr)
}
func (rm *Removed) RemoveAttribute(name string) bool {
	if rm == nil {
		return false
	}
	return rm.block.body.RemoveAttribute(name)
}
func (rm *Removed) Blocks() []Block {
	if rm == nil {
		return nil
	}
	return rm.block.body.Blocks()
}
func (rm *Removed) Block(blockType string, labels ...string) Block {
	if rm == nil {
		return nil
	}
	return rm.block.body.Block(blockType, labels...)
}
func (rm *Removed) AddBlock(blockType string, labels ...string) *Generic {
	if rm == nil {
		return nil
	}
	return rm.block.body.AddBlock(blockType, labels...)
}
func (rm *Removed) Comments() []*Comment {
	if rm == nil {
		return nil
	}
	return rm.block.body.Comments()
}
func (rm *Removed) AppendComment(text string) *Comment {
	if rm == nil {
		return nil
	}
	return rm.block.body.AppendComment(text)
}
func (rm *Removed) Items() []Item {
	if rm == nil {
		return nil
	}
	return rm.block.body.Items()
}
func (rm *Removed) Remove(target Item) bool {
	if rm == nil {
		return false
	}
	return rm.block.body.Remove(target)
}
func (i *Import) Attribute(name string) *Attribute {
	if i == nil {
		return nil
	}
	return i.block.body.Attribute(name)
}
func (i *Import) Attributes() []*Attribute {
	if i == nil {
		return nil
	}
	return i.block.body.Attributes()
}
func (i *Import) SetAttribute(name, expr string) *Attribute {
	if i == nil {
		return nil
	}
	return i.block.body.SetAttribute(name, expr)
}
func (i *Import) RemoveAttribute(name string) bool {
	if i == nil {
		return false
	}
	return i.block.body.RemoveAttribute(name)
}
func (i *Import) Blocks() []Block {
	if i == nil {
		return nil
	}
	return i.block.body.Blocks()
}
func (i *Import) Block(blockType string, labels ...string) Block {
	if i == nil {
		return nil
	}
	return i.block.body.Block(blockType, labels...)
}
func (i *Import) AddBlock(blockType string, labels ...string) *Generic {
	if i == nil {
		return nil
	}
	return i.block.body.AddBlock(blockType, labels...)
}
func (i *Import) Comments() []*Comment {
	if i == nil {
		return nil
	}
	return i.block.body.Comments()
}
func (i *Import) AppendComment(text string) *Comment {
	if i == nil {
		return nil
	}
	return i.block.body.AppendComment(text)
}
func (i *Import) Items() []Item {
	if i == nil {
		return nil
	}
	return i.block.body.Items()
}
func (i *Import) Remove(target Item) bool {
	if i == nil {
		return false
	}
	return i.block.body.Remove(target)
}
func (g *Generic) Attribute(name string) *Attribute {
	if g == nil {
		return nil
	}
	return g.block.body.Attribute(name)
}
func (g *Generic) Attributes() []*Attribute {
	if g == nil {
		return nil
	}
	return g.block.body.Attributes()
}
func (g *Generic) SetAttribute(name, expr string) *Attribute {
	if g == nil {
		return nil
	}
	return g.block.body.SetAttribute(name, expr)
}
func (g *Generic) RemoveAttribute(name string) bool {
	if g == nil {
		return false
	}
	return g.block.body.RemoveAttribute(name)
}
func (g *Generic) Blocks() []Block {
	if g == nil {
		return nil
	}
	return g.block.body.Blocks()
}
func (g *Generic) Block(blockType string, labels ...string) Block {
	if g == nil {
		return nil
	}
	return g.block.body.Block(blockType, labels...)
}
func (g *Generic) AddBlock(blockType string, labels ...string) *Generic {
	if g == nil {
		return nil
	}
	return g.block.body.AddBlock(blockType, labels...)
}
func (g *Generic) Comments() []*Comment {
	if g == nil {
		return nil
	}
	return g.block.body.Comments()
}
func (g *Generic) AppendComment(text string) *Comment {
	if g == nil {
		return nil
	}
	return g.block.body.AppendComment(text)
}
func (g *Generic) Items() []Item {
	if g == nil {
		return nil
	}
	return g.block.body.Items()
}
func (g *Generic) Remove(target Item) bool {
	if g == nil {
		return false
	}
	return g.block.body.Remove(target)
}
