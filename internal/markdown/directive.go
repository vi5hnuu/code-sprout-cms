package markdown

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Directive syntax follows the generic directives proposal used by remark-directive, so the
// web renderer (remark) and this validator read documents identically:
//
//	::name[label]{key=value key2="quoted value"}     leaf: one line, no children
//
//	:::name[label]{attrs}                            container: holds Markdown blocks
//	...
//	:::
//
// A container closes on a line of at least as many colons as opened it, so nesting uses a
// longer outer fence (:::: around :::). Text-level (single-colon) directives are not supported.

var KindDirective = ast.NewNodeKind("Directive")

type DirectiveForm int

const (
	LeafDirective DirectiveForm = iota
	ContainerDirective
)

type Directive struct {
	ast.BaseBlock
	Form  DirectiveForm
	Name  string
	Label string
	Attrs map[string]string
	// AttrErr is set when the {...} block could not be parsed; the analyzer reports it.
	AttrErr string
	fence   int // colon count of the opening line (containers only)
	// Offset of the opening line in the source, for error positions.
	Offset int
}

func (d *Directive) Kind() ast.NodeKind { return KindDirective }

func (d *Directive) IsRaw() bool { return false }

func (d *Directive) Dump(source []byte, level int) {
	ast.DumpHelper(d, source, level, map[string]string{"Name": d.Name, "Label": d.Label}, nil)
}

type directiveParser struct{}

func (directiveParser) Trigger() []byte { return []byte{':'} }

func (directiveParser) Open(_ ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, seg := reader.PeekLine()
	pos := pc.BlockOffset()
	if pos < 0 {
		return nil, parser.NoChildren
	}
	colons := 0
	for pos+colons < len(line) && line[pos+colons] == ':' {
		colons++
	}
	if colons < 2 {
		return nil, parser.NoChildren
	}
	head, ok := parseDirectiveHead(util.TrimRightSpace(line[pos+colons:]))
	if !ok {
		return nil, parser.NoChildren
	}
	d := &Directive{Name: head.name, Label: head.label, Attrs: head.attrs, AttrErr: head.attrErr, Offset: seg.Start}
	reader.AdvanceToEOL()
	if colons == 2 {
		d.Form = LeafDirective
		return d, parser.NoChildren
	}
	d.Form = ContainerDirective
	d.fence = colons
	return d, parser.HasChildren
}

func (directiveParser) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	d := node.(*Directive)
	if d.Form == LeafDirective {
		return parser.Close
	}
	line, _ := reader.PeekLine()
	if isClosingFence(line, d.fence) && !insideOpenCodeBlock(d, pc) {
		reader.AdvanceToEOL()
		return parser.Close
	}
	return parser.Continue | parser.HasChildren
}

func (directiveParser) Close(ast.Node, text.Reader, parser.Context) {}

func (directiveParser) CanInterruptParagraph() bool { return true }

func (directiveParser) CanAcceptIndentedLine() bool { return false }

func isClosingFence(line []byte, fence int) bool {
	line = util.TrimLeftSpace(util.TrimRightSpace(line))
	if len(line) < fence {
		return false
	}
	for _, c := range line {
		if c != ':' {
			return false
		}
	}
	return true
}

// goldmark offers a line to outer blocks before inner ones, so without this check a ":::"
// line inside a fenced code block (e.g. a snippet that prints colons) would close the
// surrounding container. Code content is literal; it never closes anything.
func insideOpenCodeBlock(d *Directive, pc parser.Context) bool {
	opened := pc.OpenedBlocks()
	for i, b := range opened {
		if b.Node != d {
			continue
		}
		for _, inner := range opened[i+1:] {
			if inner.Node.Kind() == ast.KindFencedCodeBlock {
				return true
			}
		}
		return false
	}
	return false
}

type directiveExtension struct{}

func (directiveExtension) Extend(m goldmark.Markdown) {
	// Priority just ahead of fenced code (700) so "::" is claimed before paragraph parsing.
	m.Parser().AddOptions(parser.WithBlockParsers(util.Prioritized(directiveParser{}, 650)))
}
