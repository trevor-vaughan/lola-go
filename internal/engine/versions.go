package engine

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"

	"golang.org/x/mod/semver"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targetspec"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// VersionQuestion is one multi-era target a scope installs for.
type VersionQuestion struct {
	Target    string
	Eras      []string // since values, newest first
	Labels    []string // ">=2.0", ">=1.0 <2.0", aligned with Eras
	Summaries []string // what each era reads differently, from its spec ("" for none), aligned with Eras
	// Drops is, aligned with Eras, the frontmatter values of the modules installed that the era
	// drops and another era keeps, each "<artifact>'s <key> <value>", safe to print.
	Drops     [][]string
	Installed string // probed version, "" when not installed or unreadable
	Preselect string // the era to preselect ("" for none): the declared one, else the installed one at a standard scope
	Declared  string // the version the scope declares, "" for none
	Standard  bool
	// installedEra is the installed version's era at a standard scope, "" elsewhere: picking it
	// records the installed version.
	installedEra string
}

// VersionChoices are the questions for ids at sc: targets with more than one era, in ids' order,
// each a scope declares a version for with it in Declared. It probes installed versions. A
// standard scope is the user scope or the project lola runs in (FoundHere): there the installed
// assistant is the one the scope's files are read by, so its era is preselected; at another
// directory it may not be. modules are the names of the modules installed, as from lists them
// (nil for none), whose values each era drops are its Drops.
func (e *Engine) VersionChoices(ctx context.Context, sc Scope, ids []string, from *Choices, modules []string) ([]VersionQuestion, error) {
	tv, _, err := e.declared(sc)
	if err != nil {
		return nil, err
	}
	standard := sc.Kind == target.User || e.FoundHere(sc)
	probes := e.newProbeRun(ctx)
	var out []VersionQuestion
	for _, id := range ids {
		if len(e.Registry.Eras(id)) < 2 {
			continue
		}
		t, err := e.target(id)
		if err != nil {
			return nil, err
		}
		q := VersionQuestion{Target: id, Standard: standard, Declared: tv[id]}
		for _, x := range e.eras(id) {
			q.Eras, q.Labels, q.Summaries = append(q.Eras, x.since), append(q.Labels, x.label), append(q.Summaries, x.summary)
			q.Drops = append(q.Drops, e.eraDrops(sc, id, x.since, from, modules))
		}
		if v, _ := probes.read(t); targetspec.Canonical(v) != "" {
			q.Installed = v
			if since, before, ok := e.Registry.Era(id, v); standard && ok && !before {
				q.Preselect, q.installedEra = since, since
			}
		}
		if q.Declared != "" {
			q.Preselect, _, _ = e.Registry.Era(id, q.Declared)
		}
		out = append(out, q)
	}
	return out, nil
}

// eraDrops are the frontmatter values of the skills, commands and agents of modules, as from
// lists them, that id's era since drops and another era of id keeps (keptBy), each
// "<artifact>'s <key> <value>", once, in module and artifact order.
func (e *Engine) eraDrops(sc Scope, id, since string, from *Choices, modules []string) []string {
	t, ok := e.Registry.For(id, since)
	if from == nil || !ok {
		return nil
	}
	pctx := target.PlanContext{Scope: sc.Kind}
	var out []string
	for _, name := range modules {
		v, ok := from.versions[name]
		if !ok || v.Module == nil {
			continue
		}
		m := v.Module
		type artifact struct {
			kind, name string
			doc        module.Doc
		}
		var arts []artifact
		for _, s := range m.Skills {
			arts = append(arts, artifact{"skill", s.Name, s.Doc})
		}
		for _, c := range m.Commands {
			arts = append(arts, artifact{"command", c.Name, c.Doc})
		}
		for _, a := range m.Agents {
			arts = append(arts, artifact{"agent", a.Name, a.Doc})
		}
		for _, a := range arts {
			_, fs, _ := target.Admit(id, t.Capabilities(), pctx, a.kind, a.name, a.doc)
			for _, f := range fs {
				if f.Key == "" || f.Severity != target.SeverityWarn {
					continue
				}
				if _, kept := e.keptBy(id, since, pctx, a.kind, a.name, a.doc, f.Key); kept {
					drop := fmt.Sprintf("%s's %s %s", untrusted.Text(a.name, untrusted.MaxName), untrusted.Text(f.Key, untrusted.MaxName), frontmatterValue(a.doc.Frontmatter[f.Key]))
					if !slices.Contains(out, drop) {
						out = append(out, drop)
					}
				}
			}
		}
	}
	return out
}

// era is one era of a target: its since, its label (">=2.0" for the newest, ">=1.0 <2.0" for
// the others) and its spec's summary.
type era struct{ since, label, summary string }

// eras are id's eras, newest first; none when id is not registered.
func (e *Engine) eras(id string) []era {
	sinces, summaries := e.Registry.Eras(id), e.Registry.Summaries(id)
	var out []era
	for i := len(sinces) - 1; i >= 0; i-- {
		label := ">=" + sinces[i]
		if i < len(sinces)-1 {
			label = ">=" + sinces[i] + " <" + sinces[i+1]
		}
		out = append(out, era{sinces[i], label, untrusted.Text(summaries[i], untrusted.MaxName)})
	}
	return out
}

// eraOf is id's era that version is written for, the first for a version below it; its since
// alone for an id lola has no target for.
func (e *Engine) eraOf(id, version string) era {
	since, _, _ := e.Registry.Era(id, version)
	for _, x := range e.eras(id) {
		if x.since == since {
			return x
		}
	}
	return era{since: since, label: since}
}

// WrittenFor is, by target with more than one era, the era (its since) the modules installed at sc,
// but skip, that install for it are written for: an install for another era is refused unless
// forced (InstallAll). A target none of them installs for, or whose modules are written for
// different eras already, is absent.
func (e *Engine) WrittenFor(sc Scope, skip []string) (map[string]string, error) {
	led, err := e.Ledger(sc)
	if err != nil {
		return nil, err
	}
	out, mixed := map[string]string{}, map[string]bool{}
	for name, ent := range led.Modules {
		if slices.Contains(skip, name) {
			continue
		}
		for _, id := range ent.Targets {
			eras := e.eras(id)
			if len(eras) < 2 {
				continue
			}
			since := e.eraOf(id, cmp.Or(ent.Eras[id], eras[0].since)).since
			if was, ok := out[id]; ok && was != since {
				mixed[id] = true
			}
			out[id] = since
		}
	}
	for id := range mixed {
		delete(out, id)
	}
	return out, nil
}

// Recorded is the value to record when the user picks era since in q: the version declared when
// it is the declared one's era (Preselect), the installed version when it is the installed era at
// a standard scope, else since.
func (q VersionQuestion) Recorded(since string) string {
	switch {
	case since == "":
	case q.Declared != "" && since == q.Preselect:
		return q.Declared
	case since == q.installedEra:
		return q.Installed
	}
	return since
}

// Records is what an install that writes for versions at sc records in target_versions: each
// version of a target with more than one era that sc does not declare already (compared as
// versions), by target id, and the file it goes in, as findings name it. A target with one era
// writes the same files for every version: nothing is recorded for it.
func (e *Engine) Records(sc Scope, versions map[string]string) (map[string]string, string, error) {
	tv, file, err := e.declared(sc)
	if err != nil {
		return nil, "", err
	}
	out := map[string]string{}
	for id, v := range versions {
		if len(e.Registry.Eras(id)) > 1 && semver.Compare(targetspec.Canonical(tv[id]), targetspec.Canonical(v)) != 0 {
			out[id] = v
		}
	}
	return out, file, nil
}

// DefaultVersions is what a run that does not ask records for ids at sc (the no-terminal rule),
// or the coached refusal at another directory. At a standard scope it records the installed
// version, and nothing when the assistant is not installed (lola writes for the newest era);
// another directory is refused, since this machine's assistant says nothing of the one reading
// that project. The refusal hints the run for each era (EraHints).
func (e *Engine) DefaultVersions(ctx context.Context, sc Scope, ids []string, rerun func(id, since string) string) (map[string]string, error) {
	qs, err := e.VersionChoices(ctx, sc, ids, nil, nil)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	var names, hints []string
	for _, q := range qs {
		switch {
		case q.Declared != "":
		case !q.Standard:
			names, hints = append(names, q.Target), append(hints, e.EraHints(q.Target, "", "", rerun)...)
		case q.Preselect != "":
			out[q.Target] = q.Installed
		}
	}
	if len(names) == 0 {
		return out, nil
	}
	dir, which := untrusted.Text(sc.ProjectDir, untrusted.MaxName), andJoin(names)
	noun := "version"
	if len(names) > 1 {
		noun = "versions"
	}
	return nil, errs.WithHints(fmt.Errorf("which %s %s will read %s? lola assumes the installed %s only for the project you run it in, and %s declares none",
		which, noun, dir, which, dir), hints...)
}

// CheckVersions refuses a version an install is given for a target lola has none for, one that is
// not a version, or, for a target with more than one era, one older than its first, naming the -t
// value it came from, with the run for each era (EraHints).
func (e *Engine) CheckVersions(versions map[string]string, rerun func(id, since string) string) error {
	for _, id := range slices.Sorted(maps.Keys(versions)) {
		if _, err := e.target(id); err != nil {
			return err
		}
		v := versions[id]
		shown := untrusted.Text(v, untrusted.MaxName)
		if targetspec.Canonical(v) == "" {
			return errs.WithHints(fmt.Errorf("-t %s@%s: %q is not a version", id, shown, shown), e.EraHints(id, "", "", rerun)...)
		}
		if _, before, _ := e.Registry.Era(id, v); before && len(e.Registry.Eras(id)) > 1 {
			return errs.WithHints(fmt.Errorf("-t %s@%s: %s %s is older than any %s lola knows", id, shown, id, shown, id), e.EraHints(id, "", "", rerun)...)
		}
	}
	return nil
}

// EraHints are the hints that write for each era of target id but except ("" for none), newest
// first: "<lead>for <id> <label>: <run>", where run is rerun(id, since), the command as typed
// giving id's version since, or, rerun nil, the -t alone; for a target with one era, the run with
// no version for it (since "").
func (e *Engine) EraHints(id, lead, except string, rerun func(id, since string) string) []string {
	eras := e.eras(id)
	if len(eras) == 1 { // one era: the version changes nothing
		run := "-t " + id
		if rerun != nil {
			run = rerun(id, "")
		}
		return []string{lead + run}
	}
	var out []string
	for _, x := range eras {
		if x.since == except {
			continue
		}
		run := "-t " + id + "@" + x.since
		if rerun != nil {
			run = rerun(id, x.since)
		}
		out = append(out, fmt.Sprintf("%sfor %s %s: %s", lead, id, x.label, run))
	}
	return out
}
