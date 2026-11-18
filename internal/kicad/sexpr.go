package kicad

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Node represents either an atom (symbol, string, number) or a parenthesized list in an S-expression AST.
type Node struct {
	IsAtom   bool
	Value    string
	Quoted   bool
	Children []*Node
}

// Atom creates an unquoted atom node.
func Atom(val string) *Node {
	return &Node{IsAtom: true, Value: val, Quoted: false}
}

// QString creates a quoted string atom node.
func QString(val string) *Node {
	return &Node{IsAtom: true, Value: val, Quoted: true}
}

// List creates a list node with the provided children.
func List(children ...*Node) *Node {
	return &Node{IsAtom: false, Children: children}
}

// Clone returns a deep copy of the Node.
func (n *Node) Clone() *Node {
	if n == nil {
		return nil
	}
	c := &Node{
		IsAtom: n.IsAtom,
		Value:  n.Value,
		Quoted: n.Quoted,
	}
	if len(n.Children) > 0 {
		c.Children = make([]*Node, len(n.Children))
		for i, ch := range n.Children {
			c.Children[i] = ch.Clone()
		}
	}
	return c
}

// Tag returns the first atom value of a list node (e.g., "symbol", "property", "pad"), or "".
func (n *Node) Tag() string {
	if n == nil || n.IsAtom || len(n.Children) == 0 || !n.Children[0].IsAtom {
		return ""
	}
	return n.Children[0].Value
}

// Arg returns the string value of child index idx (1-based argument after tag is idx=1), or "".
func (n *Node) Arg(idx int) string {
	if n == nil || n.IsAtom || idx < 0 || idx >= len(n.Children) {
		return ""
	}
	if !n.Children[idx].IsAtom {
		return ""
	}
	return n.Children[idx].Value
}

// FindChild returns the first direct child list with the matching tag.
func (n *Node) FindChild(tag string) *Node {
	if n == nil || n.IsAtom {
		return nil
	}
	for _, ch := range n.Children {
		if ch.Tag() == tag {
			return ch
		}
	}
	return nil
}

// FindChildren returns all direct child lists with the matching tag.
func (n *Node) FindChildren(tag string) []*Node {
	if n == nil || n.IsAtom {
		return nil
	}
	var out []*Node
	for _, ch := range n.Children {
		if ch.Tag() == tag {
			out = append(out, ch)
		}
	}
	return out
}

// RemoveChildren deletes all direct child lists with the matching tag.
func (n *Node) RemoveChildren(tag string) {
	if n == nil || n.IsAtom {
		return
	}
	kept := n.Children[:0]
	for _, ch := range n.Children {
		if ch.Tag() != tag {
			kept = append(kept, ch)
		}
	}
	n.Children = kept
}

// FindProperty returns the (property "<key>" "<value>" ...) child node matching key (case-sensitive, fallback case-insensitive).
func (n *Node) FindProperty(key string) *Node {
	if n == nil || n.IsAtom {
		return nil
	}
	for _, ch := range n.Children {
		if ch.Tag() == "property" && len(ch.Children) >= 3 && ch.Children[1].IsAtom && ch.Children[1].Value == key {
			return ch
		}
	}
	for _, ch := range n.Children {
		if ch.Tag() == "property" && len(ch.Children) >= 3 && ch.Children[1].IsAtom && strings.EqualFold(ch.Children[1].Value, key) {
			return ch
		}
	}
	return nil
}

// GetPropertyValue returns the value of a (property "<key>" "<val>") node, or "".
func (n *Node) GetPropertyValue(key string) string {
	p := n.FindProperty(key)
	if p == nil || len(p.Children) < 3 {
		return ""
	}
	return p.Children[2].Value
}

// SetOrAddProperty updates an existing (property "<key>" ...) node or appends a hidden KiCad property node.
func (n *Node) SetOrAddProperty(key, val string) {
	if n == nil || n.IsAtom {
		return
	}
	if prop := n.FindProperty(key); prop != nil && len(prop.Children) >= 3 {
		prop.Children[2] = QString(val)
		return
	}

	propNode := List(
		Atom("property"),
		QString(key),
		QString(val),
		List(Atom("at"), Atom("0"), Atom("0"), Atom("0")),
		List(
			Atom("effects"),
			List(
				Atom("font"),
				List(Atom("size"), Atom("1.27"), Atom("1.27")),
			),
			List(Atom("hide"), Atom("yes")),
		),
	)

	// Insert after last existing property node if present, otherwise append.
	insertIdx := len(n.Children)
	for i := len(n.Children) - 1; i >= 0; i-- {
		if n.Children[i].Tag() == "property" {
			insertIdx = i + 1
			break
		}
	}
	n.Children = append(n.Children, nil)
	copy(n.Children[insertIdx+1:], n.Children[insertIdx:])
	n.Children[insertIdx] = propNode
}

// ParseSExpr parses a single root S-expression from input.
func ParseSExpr(input string) (*Node, error) {
	nodes, err := ParseAllSExpr(input)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, errors.New("empty s-expression input")
	}
	return nodes[0], nil
}

// ParseAllSExpr parses one or more top-level S-expressions from input.
func ParseAllSExpr(input string) ([]*Node, error) {
	p := &sexprParser{src: input}
	var roots []*Node
	for {
		p.skipWSAndComments()
		if p.pos >= len(p.src) {
			break
		}
		node, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		roots = append(roots, node)
	}
	return roots, nil
}

type sexprParser struct {
	src string
	pos int
}

func (p *sexprParser) skipWSAndComments() {
	for p.pos < len(p.src) {
		b := p.src[p.pos]
		if b == ';' {
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
			continue
		}
		if unicode.IsSpace(rune(b)) {
			p.pos++
			continue
		}
		break
	}
}

func (p *sexprParser) parseValue() (*Node, error) {
	p.skipWSAndComments()
	if p.pos >= len(p.src) {
		return nil, errors.New("unexpected end of s-expression")
	}
	switch p.src[p.pos] {
	case '(':
		return p.parseList()
	case ')':
		return nil, fmt.Errorf("unexpected ')' at byte %d", p.pos)
	case '"':
		return p.parseQuotedString()
	default:
		return p.parseAtom()
	}
}

func (p *sexprParser) parseList() (*Node, error) {
	p.pos++ // consume '('
	node := &Node{IsAtom: false}
	for {
		p.skipWSAndComments()
		if p.pos >= len(p.src) {
			return nil, errors.New("unclosed '(' in s-expression")
		}
		if p.src[p.pos] == ')' {
			p.pos++
			return node, nil
		}
		child, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		node.Children = append(node.Children, child)
	}
}

func (p *sexprParser) parseQuotedString() (*Node, error) {
	p.pos++ // consume opening '"'
	var sb strings.Builder
	for p.pos < len(p.src) {
		ch := p.src[p.pos]
		if ch == '"' {
			p.pos++
			return &Node{IsAtom: true, Value: sb.String(), Quoted: true}, nil
		}
		if ch == '\\' {
			p.pos++
			if p.pos >= len(p.src) {
				return nil, errors.New("unterminated escape sequence in string")
			}
			esc := p.src[p.pos]
			p.pos++
			switch esc {
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case '"':
				sb.WriteByte('"')
			case '\\':
				sb.WriteByte('\\')
			default:
				sb.WriteByte(esc)
			}
			continue
		}
		sb.WriteByte(ch)
		p.pos++
	}
	return nil, errors.New("unclosed string literal in s-expression")
}

func (p *sexprParser) parseAtom() (*Node, error) {
	start := p.pos
	for p.pos < len(p.src) {
		ch := p.src[p.pos]
		if ch == '(' || ch == ')' || ch == '"' || unicode.IsSpace(rune(ch)) {
			break
		}
		p.pos++
	}
	if p.pos == start {
		return nil, fmt.Errorf("unexpected character %q at byte %d", p.src[p.pos], p.pos)
	}
	return &Node{IsAtom: true, Value: p.src[start:p.pos], Quoted: false}, nil
}

// String renders the S-expression compactly on a single line.
func (n *Node) String() string {
	if n == nil {
		return ""
	}
	if n.IsAtom {
		if n.Quoted {
			return quoteSExprString(n.Value)
		}
		return n.Value
	}
	var sb strings.Builder
	sb.WriteByte('(')
	for i, ch := range n.Children {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(ch.String())
	}
	sb.WriteByte(')')
	return sb.String()
}

// PrettyString renders the S-expression using KiCad-style tab indentation.
func (n *Node) PrettyString(indent int) string {
	if n == nil {
		return ""
	}
	var sb strings.Builder
	n.writePretty(&sb, indent)
	return sb.String()
}

func (n *Node) writePretty(sb *strings.Builder, indent int) {
	if n.IsAtom {
		if n.Quoted {
			sb.WriteString(quoteSExprString(n.Value))
		} else {
			sb.WriteString(n.Value)
		}
		return
	}

	if n.isInlineCandidate() {
		sb.WriteString(n.String())
		return
	}

	sb.WriteByte('(')
	wroteFirst := false
	for i, ch := range n.Children {
		if i == 0 && ch.IsAtom {
			ch.writePretty(sb, indent)
			wroteFirst = true
			continue
		}
		// Keep simple scalar arguments directly after the tag on the opening line (e.g. `(symbol "Foo"`)
		if i == 1 && wroteFirst && ch.IsAtom && len(n.Children) > 2 {
			sb.WriteByte(' ')
			ch.writePretty(sb, indent)
			continue
		}
		if i == 2 && wroteFirst && n.Children[1].IsAtom && ch.IsAtom && len(n.Children) > 3 && (n.Tag() == "property" || n.Tag() == "pad") {
			sb.WriteByte(' ')
			ch.writePretty(sb, indent)
			continue
		}
		if !wroteFirst && i == 0 {
			ch.writePretty(sb, indent+1)
			wroteFirst = true
			continue
		}
		sb.WriteByte('\n')
		for t := 0; t < indent+1; t++ {
			sb.WriteByte('\t')
		}
		ch.writePretty(sb, indent+1)
	}
	sb.WriteByte('\n')
	for t := 0; t < indent; t++ {
		sb.WriteByte('\t')
	}
	sb.WriteByte(')')
}

func (n *Node) isInlineCandidate() bool {
	if len(n.Children) == 0 {
		return true
	}
	hasNestedList := false
	approxLen := 2
	for _, ch := range n.Children {
		if !ch.IsAtom {
			hasNestedList = true
			break
		}
		approxLen += len(ch.Value) + 3
	}
	if !hasNestedList && approxLen <= 96 {
		return true
	}
	return false
}

func quoteSExprString(s string) string {
	var sb strings.Builder
	sb.Grow(len(s) + 2)
	sb.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			sb.WriteString(`\\`)
		case '"':
			sb.WriteString(`\"`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			sb.WriteByte(s[i])
		}
	}
	sb.WriteByte('"')
	return sb.String()
}
