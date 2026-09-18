package tfwrite

import "strings"

// Bytes renders the file. Anything untouched comes back exactly as it
// was parsed — the original bytes are copied, not regenerated — so
// comments, blank lines and alignment survive. Only nodes that were
// modified, and nodes built from scratch, are printed fresh.
func (f *File) Bytes() []byte {
	if f == nil {
		return nil
	}
	var b strings.Builder
	f.body.printRoot(f.src, &b)
	out := b.String()
	// A file built rather than parsed has no original trailing newline
	// to copy, and every tool expects one. Only when something was added
	// or removed: a parsed file is reproduced exactly, final newline or
	// not.
	if f.body.dirty && out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return []byte(out)
}

// String is Bytes as a string.
func (f *File) String() string { return string(f.Bytes()) }

// printRoot prints a top-level body. Between two items that both came
// from the source and are still in their original order, the original
// separating text is copied too, which is how blank lines survive — and
// so do the text before the first item and after the last, which is
// where a file's leading blank lines and its final newline live.
func (b *Body) printRoot(src []byte, out *strings.Builder) {
	if b == nil {
		return
	}
	if first, ok := b.firstParsedSpan(); ok && first.start > 0 {
		out.WriteString(string(src[:first.start]))
	}
	b.printItems(src, 0, out)
	if last, ok := b.lastParsedSpan(); ok && last.end < len(src) {
		// Only the trailing whitespace — the file's final newline. Any
		// non-blank tail is an item that was removed from the end.
		full := src[last.end:]
		tail := full[:blankPrefix(full)]
		if b.dirty && len(tail) > 0 {
			// Something was added or removed here, so this whitespace is
			// the gap that used to lead to it rather than the file's own
			// ending. One newline, not the blank line left behind.
			out.WriteString("\n")
		} else {
			out.WriteString(string(tail))
		}
	}
}

// isBlank reports whether a stretch of source is only whitespace.
func isBlank(b []byte) bool { return blankPrefix(b) == len(b) }

// blankPrefix is how many leading bytes of b are whitespace.
func blankPrefix(b []byte) int {
	for i, c := range b {
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return i
		}
	}
	return len(b)
}

func (b *Body) firstParsedSpan() (srcSpan, bool) {
	if b == nil {
		return srcSpan{}, false
	}
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
	if b == nil || len(b.items) == 0 {
		return srcSpan{}, false
	}
	if sp := b.items[len(b.items)-1].span(); sp.valid {
		return sp, true
	}
	return srcSpan{}, false
}

func (b *Body) printItems(src []byte, indent int, out *strings.Builder) {
	if b == nil {
		return
	}
	prevEnd := -1
	for i, item := range b.items {
		sp := item.span()
		switch {
		case prevEnd >= 0 && sp.valid && sp.start >= prevEnd && isBlank(src[prevEnd:sp.start]):
			// Copy whatever separated these two in the source: newlines,
			// blank lines, the indentation of the next item.
			//
			// Only when that gap is blank. Every comment is an item in
			// its own right, so anything else between two surviving
			// items is an item that was removed — and copying the gap
			// would put it back.
			out.WriteString(string(src[prevEnd:sp.start]))
		case i > 0:
			// A separator invented rather than copied, which happens
			// between items that were not both parsed. Top-level blocks
			// get a blank line between them, the way Terraform is
			// conventionally written; everything else gets one newline.
			out.WriteString("\n")
			if indent == 0 && (isBlockItem(b.items[i-1]) || isBlockItem(item)) {
				out.WriteString("\n")
			}
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
	text := c.text
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
	out.WriteString(a.name)
	out.WriteString(" = ")
	out.WriteString(a.expr)
}

func (b *block) print(src []byte, indent int, out *strings.Builder) {
	// An untouched block prints as it was, nested contents included.
	if b.sp.valid && !b.labelsChanged && !b.body.modified() {
		out.WriteString(string(src[b.sp.start:b.sp.end]))
		return
	}

	out.WriteString(b.blockType)
	for _, l := range b.labels {
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
	if b == nil {
		return false
	}
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
		case Block:
			base := blockBase(v)
			if base.labelsChanged || !base.sp.valid || base.body.modified() {
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

// isBlockItem reports whether an item is a block, for deciding how much
// whitespace an invented separator needs.
func isBlockItem(item Item) bool {
	_, ok := item.(Block)
	return ok
}
