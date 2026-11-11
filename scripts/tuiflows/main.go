// Command tuiflows checks docs/architecture/tui-flows.md against the specs that pin it: every
// transition row must be named by a spec (It or Entry) or an "// also pinned (…): <file> "<spec>""
// pointer, and every such name must match a row. It is a lint, not a spec: run it through
// `task flows` from the repository root.
//
// A name "<surface>: <from> --<input>--> <to>" matches a row of that surface with the same from,
// input and to. The input may carry a guard, " (<guard>)", which may be shortened, and the to may
// carry a remark after ", " or " (". A pointer entry without "<surface>: " takes the surface of
// the file it is in, when every row name in that file's own specs has one surface.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const doc = "docs/architecture/tui-flows.md"

// surfaces maps each "## " section of the doc that has a transition table to the prefix its specs
// are named with.
var surfaces = map[string]string{
	"Install":                 "install",
	"Profile edit":            "profile edit",
	"Scope picker":            "scope picker",
	"Trust question":          "trust",
	"Tidy":                    "tidy",
	"Trash":                   "trash",
	"Cache clean":             "cache clean",
	"Init":                    "init",
	"Market add":              "market add",
	"Migrate":                 "migrate",
	"Hook and script consent": "consent",
	"Viewer and pager":        "viewer",
	"Accessible mode":         "accessible",
	"Form keys":               "keys",
}

type row struct {
	line                        int
	surface, from, input, guard string
	to                          string
	pinned                      bool
}

// name is a row name found in a spec file: an It or Entry description, or a pointer entry.
type name struct {
	file    string
	line    int
	surface string // "" for a pointer entry that names none
	text    string // without the surface prefix
	pointer bool
}

var (
	specRE    = regexp.MustCompile(`\b(?:It|Entry)\(("(?:[^"\\]|\\.)*")`)
	pointerRE = regexp.MustCompile(`^\s*// also pinned \((.*?)\): `)
)

func main() {
	rows, problems, err := readRows(doc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tuiflows:", err)
		os.Exit(2)
	}
	names, err := readNames("internal")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tuiflows:", err)
		os.Exit(2)
	}
	problems = append(problems, match(rows, names)...)
	for _, r := range rows {
		if !r.pinned {
			input := r.input
			if r.guard != "" {
				input += " (" + r.guard + ")"
			}
			problems = append(problems, fmt.Sprintf("%s:%d: no spec or pointer names %s: %s --%s--> %s", doc, r.line, r.surface, r.from, input, r.to))
		}
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Println(p)
		}
		fmt.Printf("%d problem(s): name each row by a spec \"<surface>: <from> --<input>--> <to>\" or an \"// also pinned (…)\" pointer (%s, Reading this page)\n", len(problems), doc)
		os.Exit(1)
	}
	fmt.Printf("ok: every one of the %d transition rows in %s is pinned\n", len(rows), doc)
}

// readRows reads the transition rows of every surface of the doc, backticks dropped, and reports
// a transition table in a section surfaces does not name.
func readRows(path string) ([]*row, []string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var rows []*row
	var problems []string
	section, inTable := "", false
	for i, l := range strings.Split(string(b), "\n") {
		n := i + 1
		switch {
		case strings.HasPrefix(l, "## "):
			section, inTable = strings.TrimSpace(l[3:]), false
		case strings.HasPrefix(l, "| from | input | guard | to |"):
			_, inTable = surfaces[section] // an unknown section's rows are reported here, not one by one
			if !inTable {
				problems = append(problems, fmt.Sprintf("%s:%d: section %q has a transition table but no spec prefix in scripts/tuiflows", path, n, section))
			}
		case inTable && !strings.HasPrefix(l, "|"):
			inTable = false
		case inTable && !strings.HasPrefix(l, "|---"):
			c := strings.Split(strings.Trim(l, "|"), "|")
			if len(c) < 4 {
				problems = append(problems, fmt.Sprintf("%s:%d: a transition row needs from, input, guard and to", path, n))
				continue
			}
			for i := range c {
				c[i] = strings.TrimSpace(strings.ReplaceAll(c[i], "`", ""))
			}
			rows = append(rows, &row{line: n, surface: surfaces[section], from: c[0], input: c[1], guard: c[2], to: c[3]})
		}
	}
	return rows, problems, nil
}

// readNames reads the row names in every spec file under dir: those of its specs whose
// description starts with a surface prefix and has an arrow, and its pointer entries. A pointer
// entry without a prefix takes the file's surface (fileSurface).
func readNames(dir string) ([]name, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }() // read-only: a failed close loses nothing
	var names []name
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(rel, "_test.go") {
			return err
		}
		b, err := fs.ReadFile(root.FS(), rel)
		if err != nil {
			return err
		}
		path := filepath.Join(dir, rel)
		var mine []name
		for i, l := range strings.Split(string(b), "\n") {
			if m := pointerRE.FindStringSubmatch(l); m != nil {
				for e := range strings.SplitSeq(m[1], "; ") {
					s, text := splitSurface(e)
					mine = append(mine, name{file: path, line: i + 1, surface: s, text: text, pointer: true})
				}
				continue
			}
			for _, m := range specRE.FindAllStringSubmatch(l, -1) {
				desc, err := strconv.Unquote(m[1])
				if err != nil {
					return fmt.Errorf("%s:%d: %w", path, i+1, err)
				}
				if s, text := splitSurface(desc); s != "" && strings.Contains(text, " --") && strings.Contains(text, "--> ") {
					mine = append(mine, name{file: path, line: i + 1, surface: s, text: text})
				}
			}
		}
		own := fileSurface(mine)
		for i := range mine {
			if mine[i].surface == "" {
				mine[i].surface = own
			}
		}
		names = append(names, mine...)
		return nil
	})
	return names, err
}

// splitSurface splits "<surface>: <rest>" for a surface the doc has; "" and s otherwise.
func splitSurface(s string) (string, string) {
	for _, p := range surfaces {
		if rest, ok := strings.CutPrefix(s, p+": "); ok {
			return p, rest
		}
	}
	return "", s
}

// fileSurface is the one surface of the file's own spec names, "" when they have none or several.
func fileSurface(names []name) string {
	own := ""
	for _, n := range names {
		switch {
		case n.pointer:
		case own == "":
			own = n.surface
		case own != n.surface:
			return ""
		}
	}
	return own
}

// match marks every row a name matches as pinned and reports each name that matches none.
func match(rows []*row, names []name) []string {
	var problems []string
	for _, n := range names {
		if n.surface == "" {
			problems = append(problems, fmt.Sprintf("%s:%d: pointer entry %q names no surface, and the file's specs name more than one (or none)", n.file, n.line, n.text))
			continue
		}
		from, rest, ok1 := strings.Cut(n.text, " --")
		input, to, ok2 := strings.Cut(rest, "--> ")
		found := false
		for _, r := range rows {
			if ok1 && ok2 && r.surface == n.surface && r.from == from &&
				(input == r.input || strings.HasPrefix(input, r.input+" (")) &&
				(to == r.to || strings.HasPrefix(to, r.to+", ") || strings.HasPrefix(to, r.to+" (")) {
				r.pinned, found = true, true
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf("%s:%d: %s: %s names no transition row in %s", n.file, n.line, n.surface, n.text, doc))
		}
	}
	return problems
}
