package tfwrite

import "strings"

// Bytes renders the file. Anything untouched comes back exactly as it
// was parsed — the original bytes are copied, not regenerated — so
// comments, blank lines and alignment survive. Only nodes that were
// modified, and nodes built from scratch, are printed fresh.
func (f *File) Bytes() []byte {
	var b strings.Builder
	f.body.printRoot(f.src, &b)
	return []byte(b.String())
}

// String is Bytes as a string.
func (f *File) String() string { return string(f.Bytes()) }

// printRoot prints a top-level body. Between two items that both came
// from the source and are still in their original order, the original
// separating text is copied too, which is how blank lines survive — and
// so do the text before the first item and after the last, which is
// where a file's leading blank lines and its final newline live.
func (b *Body) printRoot(src []byte, out *strings.Builder) {
	if first, ok := b.firstParsedSpan(); ok && first.start > 0 {
		out.WriteString(string(src[:first.start]))
	}
	b.printItems(src, 0, out)
	if last, ok := b.lastParsedSpan(); ok && last.end < len(src) {
		out.WriteString(string(src[last.end:]))
	}
}

func (b *Body) firstParsedSpan() (srcSpan, bool) {
	for _, item := range b.items {
		if sp := item.span(); sp.valid {
			return sp, true
		}
	}
	return srcSpan{}, false
}

// lastParsedSpan is the end of the last item that is still in its
// original place. An appended item has no span, and there is then no
// original tail to copy — the caller has already written past it.
func (b *Body) lastParsedSpan() (srcSpan, bool) {
	if len(b.items) == 0 {
		return srcSpan{}, false
	}
	if sp := b.items[len(b.items)-1].span(); sp.valid {
		return sp, true
	}
	return srcSpan{}, false
}

func (b *Body) printItems(src []byte, indent int, out *strings.Builder) {
	prevEnd := -1
	for i, item := range b.items {
		sp := item.span()
		switch {
		case prevEnd >= 0 && sp.valid && sp.start >= prevEnd:
			// Copy whatever separated these two in the source: newlines,
			// blank lines, the indentation of the next item.
			out.WriteString(string(src[prevEnd:sp.start]))
		case i > 0:
			out.WriteString("\n")
			out.WriteString(strings.Repeat("  ", indent))
		default:
			if indent > 0 {
				out.WriteString("\n")
				out.WriteString(strings.Repeat("  ", indent))
			}
		}
		item.print(src, indent, out)
		if sp.valid {
			prevEnd = sp.end
		} else {
			prevEnd = -1
		}
	}
}

func (c *Comment) print(src []byte, _ int, out *strings.Builder) {
	if c.sp.valid {
		out.WriteString(string(src[c.sp.start:c.sp.end]))
		return
	}
	text := c.Text
	if !strings.HasPrefix(text, "#") && !strings.HasPrefix(text, "//") && !strings.HasPrefix(text, "/*") {
		text = "# " + text
	}
	out.WriteString(text)
}

func (a *Attribute) print(src []byte, _ int, out *strings.Builder) {
	if a.sp.valid && !a.changed {
		out.WriteString(string(src[a.sp.start:a.sp.end]))
		return
	}
	out.WriteString(a.Name)
	out.WriteString(" = ")
	out.WriteString(a.Expr)
}

func (b *Block) print(src []byte, indent int, out *strings.Builder) {
	// An untouched block prints as it was, nested contents included.
	if b.sp.valid && !b.labelsChanged && !b.body.modified() {
		out.WriteString(string(src[b.sp.start:b.sp.end]))
		return
	}

	out.WriteString(b.Type)
	for _, l := range b.Labels {
		out.WriteString(" ")
		out.WriteString(quoteLabel(l))
	}
	out.WriteString(" {")
	b.body.printItems(src, indent+1, out)
	out.WriteString("\n")
	out.WriteString(strings.Repeat("  ", indent))
	out.WriteString("}")
}

// modified reports whether anything in this body, at any depth, is no
// longer as it was parsed — which is what decides between copying a
// block's original text and printing it again.
func (b *Body) modified() bool {
	if b.dirty {
		return true
	}
	for _, item := range b.items {
		switch v := item.(type) {
		case *Attribute:
			if v.changed || !v.sp.valid {
				return true
			}
		case *Comment:
			if !v.sp.valid {
				return true
			}
		case *Block:
			if v.labelsChanged || !v.sp.valid || v.body.modified() {
				return true
			}
		}
	}
	return false
}

func quoteLabel(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
