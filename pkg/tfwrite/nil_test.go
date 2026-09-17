package tfwrite

// Every lookup returns nil when it finds nothing, so every method has to
// survive a nil receiver — otherwise the first miss in a chain is a
// panic rather than a nil, and callers have to guard each link.
//
// These tests would all panic rather than fail, which is the point: a
// missed guard shows up here instead of in somebody's hook.

// TestNilFileIsSafe walks every File accessor on a nil *File.
func (s *TFWriteSuite) TestNilFileIsSafe() {
	var f *File

	s.Equal("", f.Filename())
	s.Nil(f.Body())
	s.Nil(f.Bytes())
	s.Nil(f.Blocks())
	s.False(f.Remove(nil))

	s.Nil(f.Resource("t", "n"))
	s.Nil(f.Resources(""))
	s.Nil(f.AddResource("t", "n"))
	s.Nil(f.DataSource("t", "n"))
	s.Nil(f.DataSources(""))
	s.Nil(f.AddDataSource("t", "n"))
	s.Nil(f.Ephemeral("t", "n"))
	s.Nil(f.AddEphemeral("t", "n"))
	s.Nil(f.Action("t", "n"))
	s.Nil(f.AddAction("t", "n"))
	s.Nil(f.Provider("n"))
	s.Nil(f.Providers())
	s.Nil(f.AddProvider("n"))
	s.Nil(f.Variable("n"))
	s.Nil(f.Variables())
	s.Nil(f.AddVariable("n"))
	s.Nil(f.Output("n"))
	s.Nil(f.Outputs())
	s.Nil(f.AddOutput("n"))
	s.Nil(f.Module("n"))
	s.Nil(f.Modules())
	s.Nil(f.AddModule("n"))
	s.Nil(f.Check("n"))
	s.Nil(f.Checks())
	s.Nil(f.AddCheck("n"))
	s.Nil(f.Terraform())
	s.Nil(f.AddTerraform())
	s.Nil(f.Locals())
	s.Nil(f.AddLocals())
	s.Nil(f.Moved())
	s.Nil(f.AddMoved())
	s.Nil(f.Removed())
	s.Nil(f.AddRemoved())
	s.Nil(f.Imports())
	s.Nil(f.AddImport())

	_, err := f.MarshalTree()
	s.Error(err, "marshalling nothing is an error rather than empty output")
}

// TestNilBodyIsSafe walks every Body method on a nil *Body, which is
// what f.Resource(missing).Body() hands back.
func (s *TFWriteSuite) TestNilBodyIsSafe() {
	var b *Body

	s.Nil(b.Items())
	s.Nil(b.Blocks())
	s.Nil(b.Attributes())
	s.Nil(b.Comments())
	s.Nil(b.Attribute("x"))
	s.Nil(b.SetAttribute("x", `"y"`))
	s.Nil(b.AppendComment("hi"))
	s.Nil(b.NestedBlock("lifecycle"))
	s.False(b.RemoveAttribute("x"))
	s.False(b.Remove(nil))
	s.False(b.RemoveBlock(nil))
	s.False(b.RemoveComment(nil))
	s.False(b.modified())
}

// TestNilBlocksAreSafe covers every block type: the label readers, the
// setters, Body, Delete and the interface methods. A promoted method
// would panic here, which is why each type has its own.
func (s *TFWriteSuite) TestNilBlocksAreSafe() {
	var (
		r  *Resource
		d  *DataSource
		e  *Ephemeral
		a  *Action
		p  *Provider
		v  *Variable
		o  *Output
		m  *Module
		c  *Check
		t  *Terraform
		l  *Locals
		mv *Moved
		rm *Removed
		i  *Import
		g  *Generic
	)

	// Every type: the three interface methods, Body and Delete.
	for _, blk := range []interface {
		BlockType() string
		Labels() []string
		Body() *Body
	}{r, d, e, a, p, v, o, m, c, t, l, mv, rm, i, g} {
		s.Equal("", blk.BlockType())
		s.Nil(blk.Labels())
		s.Nil(blk.Body())
	}

	s.False(r.Delete())
	s.False(d.Delete())
	s.False(e.Delete())
	s.False(a.Delete())
	s.False(p.Delete())
	s.False(v.Delete())
	s.False(o.Delete())
	s.False(m.Delete())
	s.False(c.Delete())
	s.False(t.Delete())
	s.False(l.Delete())
	s.False(mv.Delete())
	s.False(rm.Delete())
	s.False(i.Delete())
	s.False(g.Delete())

	// The typed label readers and their setters.
	s.Equal("", r.ResourceType())
	s.Equal("", r.Name())
	s.Equal("", d.DataType())
	s.Equal("", d.Name())
	s.Equal("", e.EphemeralType())
	s.Equal("", e.Name())
	s.Equal("", a.ActionType())
	s.Equal("", a.Name())
	s.Equal("", p.Name())
	s.Equal("", p.Alias())
	s.Equal("", v.Name())
	s.Equal("", o.Name())
	s.Equal("", m.Name())
	s.Equal("", m.Source())
	s.Equal("", c.Name())

	r.SetResourceType("x")
	r.SetName("x")
	d.SetDataType("x")
	d.SetName("x")
	e.SetEphemeralType("x")
	e.SetName("x")
	a.SetActionType("x")
	a.SetName("x")
	p.SetName("x")
	v.SetName("x")
	o.SetName("x")
	m.SetName("x")
	c.SetName("x")
	g.SetLabels([]string{"x"})
}

// TestNilAttributeAndCommentAreSafe covers the two leaf items.
func (s *TFWriteSuite) TestNilAttributeAndCommentAreSafe() {
	var (
		a *Attribute
		c *Comment
	)
	a.SetExpr("x")
	s.False(a.Delete())
	s.False(c.Delete())
}

// TestChainThroughAMissThrowsNothing is the case all of the above exist
// for: a lookup misses partway down and the rest of the chain still runs.
func (s *TFWriteSuite) TestChainThroughAMissThrowsNothing() {
	f, err := Parse([]byte(messyFile), "main.tf")
	s.Require().NoError(err)

	// Missing at the first link, and at each one after.
	s.False(f.Resource("nope", "nope").Body().Attribute("acl").Delete())
	s.False(f.Resource("aws_s3_bucket", "logs").Body().Attribute("nope").Delete())
	s.Equal("", f.Module("nope").Body().Attribute("source").Expr())
	s.Nil(f.Variable("nope").Body().Comments())
	s.Empty(f.Provider("nope").Labels())

	s.Equal(messyFile, f.String(), "none of it changed the file")
}
