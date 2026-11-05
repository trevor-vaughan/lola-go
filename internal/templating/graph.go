package templating

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Codes of the include graph's refusals (Plan 7 §3.3).
const (
	codeIncludeMissing = "template-include-missing"
	codeIncludeCycle   = "template-include-cycle"
	codeIncludeTooDeep = "template-include-too-deep"
)

// graph is a template's include graph for one target (Plan 7 §3.3): every partial its includes
// reach, each screened as a template, known before anything renders, because include names are
// literals.
type graph struct {
	p        Partials
	target   string
	lim      Limits
	declared []string
	root     *node
	nodes    map[string]*node // partials by file
	keys     map[string]*node // partials by the path liquid compiles them under (storeKey)
	order    []*node          // partials in the order the graph first reaches them
}

// node is the template or one partial of a graph.
type node struct {
	fileInfo
	from  string // a partial's first include site in graph (depth-first) order, <file>:<line>
	depth int    // blocks open at once in it and the partials it reaches, from its start
	chain int    // partials in its longest include chain, itself included when it is one
}

// screen screens src and every partial its includes reach for target, and refuses a cycle, a
// chain of more than lim.Chain partials, more than lim.Assigns assign tags across them, and
// blocks nested deeper than lim.Depth across them. An include with no partial for target is a
// missing leaf, with no file and no edges, which contributes to none of these: the store refuses
// it when a rendering include asks for it (Plan 7 §5.7.5).
func screen(src Source, p Partials, target string, lim Limits, declared []string) (*graph, error) {
	info, err := screenFile(src, lim, declared)
	if err != nil {
		return nil, err
	}
	g := &graph{p: p, target: target, lim: lim, declared: declared, root: &node{fileInfo: info}, nodes: map[string]*node{}, keys: map[string]*node{}}
	if err := g.visit(g.root, nil, nil); err != nil {
		return nil, err
	}
	if g.root.chain > lim.Chain {
		return nil, g.tooLong()
	}
	n := 0
	for _, f := range append([]*node{g.root}, g.order...) {
		for _, line := range f.assigns {
			if n++; n > lim.Assigns {
				return nil, f.refusal(newError(codeTooManyAssigns, f.src.File, line, "more than %d assign tags; a template and its partials have at most %d", lim.Assigns, lim.Assigns))
			}
		}
	}
	if g.root.depth > lim.Depth {
		return nil, g.tooDeep()
	}
	return g, nil
}

// refusal is err, an *Error in n, with n's first include site when n is a partial.
func (n *node) refusal(err error) error {
	if e, ok := errors.AsType[*Error](err); ok && n.from != "" {
		e.IncludedFrom = n.from
	}
	return err
}

// visit screens the partials n includes that the graph has not reached yet, depth first;
// names and files are the partials of the chain above n.
func (g *graph) visit(n *node, names, files []string) error {
	for _, mk := range n.marks {
		if !mk.include {
			n.depth = max(n.depth, mk.open)
			continue
		}
		file, ok := g.p.resolve(mk.name, g.target)
		if !ok {
			continue // a missing leaf: refused only when a rendering include asks for it (store)
		}
		if i := slices.Index(files, file); i >= 0 {
			return n.refusal(newError(codeIncludeCycle, n.src.File, mk.line, "includes %s, which includes it again (%s)",
				mk.name, strings.Join(append(slices.Clone(names[i:]), mk.name), " → ")))
		}
		child, seen := g.nodes[file]
		if !seen {
			child = &node{fileInfo: fileInfo{src: Source{Module: n.src.Module, File: file, Line: 1, Body: g.p.Files[file]}},
				from: fmt.Sprintf("%s:%d", clean(n.src.File, maxFile), mk.line)}
			if len(child.src.Body) > g.lim.Body {
				return child.refusal(newError(codeTooLarge, file, 0, "is %d bytes; a partial file is at most %s", len(child.src.Body), sizeText(g.lim.Body)))
			}
			if !utf8.ValidString(child.src.Body) {
				return child.refusal(newError(codeInvalidUTF8, file, 1+invalidLine(child.src.Body), "is not UTF-8; a partial file must be UTF-8 text"))
			}
			info, err := screenFile(child.src, g.lim, g.declared)
			if err != nil {
				return child.refusal(err)
			}
			child.fileInfo = info
			g.nodes[file], g.keys[storeKey(g.root.src.File, mk.name)], g.order = child, child, append(g.order, child)
			if err := g.visit(child, append(names, mk.name), append(files, file)); err != nil {
				return err
			}
		}
		n.depth = max(n.depth, mk.open+child.depth)
		n.chain = max(n.chain, child.chain)
	}
	if n != g.root {
		n.chain++
	}
	return nil
}

// child is the node of the partial mk includes, which visit reached; nil for a block and for a
// missing leaf.
func (g *graph) child(mk mark) *node {
	if !mk.include {
		return nil
	}
	file, _ := g.p.resolve(mk.name, g.target)
	return g.nodes[file]
}

// tooLong refuses the include that makes the root's longest chain pass lim.Chain partials,
// walking down that chain: at each node, one include leads on to the chain's length.
func (g *graph) tooLong() error {
	n, names := g.root, []string(nil)
	for {
		mk := n.marks[slices.IndexFunc(n.marks, func(mk mark) bool { c := g.child(mk); return c != nil && len(names)+c.chain > g.lim.Chain })]
		if names = append(names, mk.name); len(names) > g.lim.Chain {
			return n.refusal(newError(codeIncludeTooDeep, n.src.File, mk.line, "includes %s, which makes a chain of %d partials (%s); partials nest at most %d deep",
				mk.name, len(names), strings.Join(names, " → "), g.lim.Chain))
		}
		n = g.child(mk)
	}
}

// tooDeep refuses the block that opens past lim.Depth across the root and its partials, walking
// down the includes that hold it: at each node, one block or include leads on to the depth.
func (g *graph) tooDeep() error {
	n, base := g.root, 0
	for {
		mk := n.marks[slices.IndexFunc(n.marks, func(mk mark) bool {
			c := g.child(mk)
			return !mk.include && base+mk.open > g.lim.Depth || c != nil && base+mk.open+c.depth > g.lim.Depth
		})]
		if !mk.include {
			return n.refusal(newError(codeTooDeep, n.src.File, mk.line, "blocks nest more than %d deep", g.lim.Depth))
		}
		n, base = g.child(mk), base+mk.open
	}
}

// names lists the partials the graph reaches by name, which the store serves.
func (g *graph) names() []string {
	var out []string
	for _, n := range append([]*node{g.root}, g.order...) {
		for _, mk := range n.marks {
			if mk.include && !slices.Contains(out, mk.name) {
				out = append(out, mk.name)
			}
		}
	}
	return out
}
