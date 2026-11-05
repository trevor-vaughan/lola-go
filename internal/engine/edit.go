package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// The actions of an Edit.
const (
	EditAdded   = "added"
	EditChanged = "changed"
	EditRemoved = "removed"
)

// Edit is one entry a command wrote into a file, as its write line and JSON name it (spec
// 2026-10-29 §3.7, §9): Module; Action (added, changed or removed); the file's Profile by
// qualified name (default, user:go, project:team, or project.yml for a project's base), its Kind
// (base, user or project), Path and Scope. For human output: Label names the file (default (base),
// profile go), Created says the run created it, Shared that git tracks it, Only counts the entry's
// only: list ("4 skills") and Overrides names the layer whose entry it copied, when no override
// notice of the run says so already. Every string has been through untrusted.Text.
type Edit struct {
	Module    string `json:"module"`
	Action    string `json:"action"`
	Profile   string `json:"profile"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Scope     string `json:"scope"`
	Label     string `json:"-"`
	Created   bool   `json:"-"`
	Shared    bool   `json:"-"`
	Only      string `json:"-"`
	Overrides string `json:"-"`
}

// editsOf is the edits of entries written into t's file at sc (set; removed, the names it
// removes): added where the file did not list the module, changed where it listed it otherwise,
// and nothing for an entry the file holds as written. overrides maps a
// module to the name of the layer whose entry an override copied.
func editsOf(ctx context.Context, sc Scope, t writeTo, set []manifest.Entry, removed []string, overrides map[string]string) []Edit {
	base := Edit{Profile: shown(strings.TrimPrefix(t.Name, ledger.FromProfile(""))), Kind: t.kind(), Path: shown(t.Path),
		Scope: sc.Kind.String(), Label: t.label, Created: t.Profile && t.read == nil}
	if sc.Kind == target.Project && (!t.Profile || t.Key != "") {
		if rel, err := filepath.Rel(sc.ProjectDir, t.Path); err == nil {
			base.Shared, _ = gitTracked(ctx, sc.ProjectDir, filepath.ToSlash(rel)) // display only: unknown reads as not shared
		}
	}
	out := make([]Edit, 0, len(set)+len(removed))
	for _, ent := range set {
		ed := base
		ed.Module, ed.Action, ed.Only, ed.Overrides = shown(ent.Name), EditAdded, onlyText(ent.Only), overrides[ent.Name]
		if prev := t.layerEntry(ent.Name); prev != nil {
			if sameEntry(t.fileEntry(sc, *prev), ent) {
				continue
			}
			ed.Action = EditChanged
		}
		out = append(out, ed)
	}
	for _, m := range removed {
		ed := base
		ed.Module, ed.Action = shown(m), EditRemoved
		out = append(out, ed)
	}
	return out
}

// onlyText counts an entry's only: list by kind, as a write line says it: "4 skills", "2 skills, 1
// command"; "" for none.
func onlyText(only []string) string {
	count := map[string]int{}
	for _, s := range only {
		kind, _, _ := strings.Cut(s, "/")
		count[kind]++
	}
	var parts []string
	for _, k := range config.Kinds() {
		switch n := count[k]; {
		case n == 1:
			parts = append(parts, "1 "+k)
		case n > 1:
			parts = append(parts, fmt.Sprintf("%d %ss", n, k))
		}
	}
	return strings.Join(parts, ", ")
}

// alsoOn is the notice that t, a profile of yours a run at sc wrote, is on in the other scope the
// run knows (spec 2026-10-29 §3.9): user scope, or from user scope the project found from the
// working directory; nothing when it is not, or when the run acts on that scope too (others).
func (e *Engine) alsoOn(sc Scope, t writeTo, others []Scope) []Notice {
	if !t.Profile || t.Base || t.Key != "" {
		return nil
	}
	other, where := Scope{Kind: target.User}, "at user scope"
	if sc.Kind == target.User {
		cwd, err := e.Getwd()
		if err != nil {
			return nil
		}
		root, found := findProject(cwd)
		if !found {
			return nil
		}
		other, where = Scope{Kind: target.Project, ProjectDir: root}, "in this project"
	}
	if slices.ContainsFunc(others, func(o Scope) bool { return o.Key() == other.Key() }) {
		return nil
	}
	led, err := e.Ledger(other)
	if err != nil || led.Profile == nil || ledger.FromProfile(led.Profile.Name) != t.Name {
		return nil
	}
	return []Notice{{Level: LevelInfo, Text: strings.TrimPrefix(t.label, "profile ") + " is also on " + where + " → " + e.SyncCommand(other)}}
}
