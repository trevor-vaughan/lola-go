package engine

import (
	"bytes"
	"strings"
)

// nameMatcher finds the names of a module's components in a text in one pass, however many
// names there are (cherry-pick design §5.1: a name is found only as a whole token, bounded by the
// text's ends or a byte outside [A-Za-z0-9_-]; case matters).
//
// A name holds only [a-z0-9._-] (module.ValidateName), so it occurs as a whole token just when it
// is a run of segments inside one run of the text, a maximal run of [A-Za-z0-9_.-], where the
// segments of a run are its pieces between '.'s. The matcher is an Aho-Corasick automaton over
// segments: each segment of a name is numbered (segs), the names spell a trie of states (next,
// state 0 its root), and fail and out, computed breadth-first, make one step per segment of the
// text. A text costs one map lookup per segment plus the names found, so a module that holds many
// names, or names of many '.'s, cannot make the scan slow.
type nameMatcher struct {
	segs  map[string]int
	next  map[[2]int]int // state, segment → the state that segment leads to
	fail  []int          // per state, the state of its longest proper suffix that the trie holds
	named [][]Component  // per state, the components whose name it spells
	out   []int          // per state, the nearest state of its fail chain (itself first) that spells a name; -1 for none
}

// newNameMatcher builds the matcher of names, each with the components of that name.
func newNameMatcher(names map[string][]Component) *nameMatcher {
	type edge struct{ seg, to int }
	m := &nameMatcher{segs: map[string]int{}, next: map[[2]int]int{}, fail: []int{0}, named: [][]Component{nil}}
	kids := [][]edge{nil}
	for name, cs := range names {
		st := 0
		for seg := range strings.SplitSeq(name, ".") {
			id, ok := m.segs[seg]
			if !ok {
				id = len(m.segs)
				m.segs[seg] = id
			}
			to, ok := m.next[[2]int{st, id}]
			if !ok {
				to = len(m.fail)
				m.next[[2]int{st, id}] = to
				m.fail, m.named, kids = append(m.fail, 0), append(m.named, nil), append(kids, nil)
				kids[st] = append(kids[st], edge{id, to})
			}
			st = to
		}
		m.named[st] = cs
	}
	m.out = make([]int, len(m.fail))
	m.out[0] = -1
	for queue := []int{0}; len(queue) > 0; queue = queue[1:] {
		st := queue[0]
		for _, k := range kids[st] {
			if st != 0 {
				m.fail[k.to] = m.step(m.fail[st], k.seg)
			}
			m.out[k.to] = m.out[m.fail[k.to]]
			if m.named[k.to] != nil {
				m.out[k.to] = k.to
			}
			queue = append(queue, k.to)
		}
	}
	return m
}

// step is the state after segment seg from st: the trie's edge from st, else from the states of
// its fail chain, else the root.
func (m *nameMatcher) step(st, seg int) int {
	for {
		if to, ok := m.next[[2]int{st, seg}]; ok {
			return to
		}
		if st == 0 {
			return 0
		}
		st = m.fail[st]
	}
}

// find lists the components whose name b holds as a whole token, each name's once.
func (m *nameMatcher) find(b []byte) []Component {
	var out []Component
	found := map[int]bool{}
	for i := 0; i < len(b); {
		if !runByte(b[i]) {
			i++
			continue
		}
		j := i + 1
		for j < len(b) && runByte(b[j]) {
			j++
		}
		st := 0
		for seg := range bytes.SplitSeq(b[i:j], []byte{'.'}) {
			id, ok := m.segs[string(seg)]
			if !ok {
				st = 0 // no name holds this segment
				continue
			}
			st = m.step(st, id)
			// A state found already found every state below it on its out chain then.
			for o := m.out[st]; o >= 0 && !found[o]; o = m.out[m.fail[o]] {
				found[o] = true
				out = append(out, m.named[o]...)
			}
		}
		i = j
	}
	return out
}

// runByte reports whether c can be part of a run: a token byte (a letter, a digit, '_' or '-')
// or '.'.
func runByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-' || c == '.'
}
