package tfwrite

import (
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// Parse reads Terraform source into a tree.
//
// Structure comes from hclsyntax; comments do not, since they are not
// part of the syntax tree, so they are recovered from a separate lex and
// placed back among the items they sit between. That is the whole reason
// this package exists rather than using hclwrite directly: hclwrite
// keeps comments in its token stream but does not expose the ordered
// node list that interleaves them with attributes and blocks.
func Parse(src []byte, filename string) (*File, error) {
	syntaxFile, diags := hclsyntax.ParseConfig(src, filename, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parsing %s: %s", filename, diags.Error())
	}
	root, ok := syntaxFile.Body.(*hclsyntax.Body)
	if !ok {
		return nil, fmt.Errorf("parsing %s: unexpected body type %T", filename, syntaxFile.Body)
	}

	comments, err := lexComments(src, filename)
	if err != nil {
		return nil, err
	}

	f := &File{filename: filename, src: src}
	f.body = buildBody(src, root, comments, len(src))
	return f, nil
}

// lexComments pulls every comment token out of the source with its byte
// range, so buildBody can slot them back in among the items.
func lexComments(src []byte, filename string) ([]*Comment, error) {
	tokens, diags := hclsyntax.LexConfig(src, filename, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("lexing %s: %s", filename, diags.Error())
	}
	var out []*Comment
	for _, t := range tokens {
		if t.Type != hclsyntax.TokenComment {
			continue
		}
		// A line comment's token runs to the end of the line, newline
		// included. The span has to stop short of it, or printing the
		// comment emits a newline that whoever prints the next thing
		// will emit again.
		text := string(t.Bytes)
		trimmed := trimTrailingNewline(text)
		out = append(out, &Comment{
			Text: trimmed,
			sp: srcSpan{
				start: t.Range.Start.Byte,
				end:   t.Range.End.Byte - (len(text) - len(trimmed)),
				valid: true,
			},
		})
	}
	return out, nil
}

// buildBody assembles one body's ordered items. Attributes come out of
// hclsyntax as a map, so they are sorted back into source order; blocks
// are already ordered but are sorted with them; and every comment that
// falls inside this body but not inside one of its nested blocks belongs
// here, between whichever items it lands among.
func buildBody(src []byte, syntax *hclsyntax.Body, comments []*Comment, bodyEnd int) *Body {
	b := &Body{src: src}

	type placed struct {
		start int
		item  Item
	}
	var placedItems []placed

	// Ranges already accounted for by a child item, so a comment inside
	// one is not also collected here. Two kinds:
	//
	//   - a nested block, whose own body claims it;
	//   - an attribute's expression, which can contain comments of its
	//     own inside a `{ ... }` or a list. Those bytes are part of the
	//     attribute's text, so collecting them again would print the
	//     comment twice as soon as anything made the body reprint.
	type childRange struct{ start, end int }
	var claimed []childRange

	for _, blk := range syntax.Blocks {
		r := blk.Range()
		claimed = append(claimed, childRange{r.Start.Byte, r.End.Byte})
		inner := buildBody(src, blk.Body, comments, blk.Body.EndRange.End.Byte)
		placedItems = append(placedItems, placed{r.Start.Byte, newBlock(
			blk.Type,
			append([]string(nil), blk.Labels...),
			inner,
			srcSpan{start: r.Start.Byte, end: r.End.Byte, valid: true},
		)})
	}

	for _, attr := range syntax.Attributes {
		r := attr.SrcRange
		exprRange := attr.Expr.Range()
		claimed = append(claimed, childRange{exprRange.Start.Byte, exprRange.End.Byte})
		placedItems = append(placedItems, placed{r.Start.Byte, &Attribute{
			Name: attr.Name,
			Expr: string(src[exprRange.Start.Byte:exprRange.End.Byte]),
			sp:   srcSpan{start: r.Start.Byte, end: r.End.Byte, valid: true},
		}})
	}

	bodyStart := syntax.SrcRange.Start.Byte
	for _, c := range comments {
		if c.sp.start < bodyStart || c.sp.end > bodyEnd {
			continue
		}
		inChild := false
		for _, cb := range claimed {
			if c.sp.start >= cb.start && c.sp.end <= cb.end {
				inChild = true
				break
			}
		}
		if inChild {
			continue
		}
		placedItems = append(placedItems, placed{c.sp.start, &Comment{Text: c.Text, sp: c.sp}})
	}

	sort.SliceStable(placedItems, func(i, j int) bool { return placedItems[i].start < placedItems[j].start })
	for _, p := range placedItems {
		adopt(b, p.item)
		b.items = append(b.items, p.item)
	}
	return b
}

func trimTrailingNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// adopt records which body an item belongs to, so the item can remove
// itself later.
func adopt(b *Body, item Item) {
	switch v := item.(type) {
	case *Comment:
		v.parent = b
	case *Attribute:
		v.parent = b
	case Block:
		blockBase(v).parent = b
	}
}
