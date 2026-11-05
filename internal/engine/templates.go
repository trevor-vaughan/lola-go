package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/templating"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Codes of the refusals lola makes after rendering a template (Plan 7 §5.6, §11.2).
const (
	codeTemplateFrontmatter = "template-frontmatter"
	codeTemplateFence       = "template-frontmatter-fence"
	codeTemplateName        = "template-name-mismatch"
	codeTemplateTimeout     = "template-timeout"
	codeRenderedCheck       = "template-rendered-check"
	codeTemplateRunBudget   = "template-run-budget"
)

// TemplateBudget caps the rendering of one plan pass (Plan 7 §6.2): RunTime of rendering and
// RunOutput bytes of rendered files held at once.
type TemplateBudget struct {
	RunOutput int
	RunTime   time.Duration
}

// TemplateError is a template that refused a run (Plan 7 §11): the module, the target and scope
// it rendered for, the component (Kind, Artifact) and its file, with the line, the code and the
// reason. Every string is author text that passed untrusted.Text. IncludedFrom is the include
// site of an error in a partial, "" otherwise.
type TemplateError struct {
	Module, Target, Scope, Kind, Artifact, File string
	Line                                        int
	Code, Reason, IncludedFrom                  string
	rendered                                    bool     // a refusal of the rendered output, not of the template
	merged                                      bool     // a rendered refusal of what an attribute file merged
	hints                                       []string // a rendered check's coaching, which check's finding keeps
}

func (e *TemplateError) Error() string {
	if e.Code == codeTemplateRunBudget {
		return e.Module + ": " + e.Reason
	}
	return fmt.Sprintf("%s: %s %s %s: %s", e.Module, e.Target, e.Kind, e.Artifact, e.text())
}

// text is the refusal without its module and component: "<file>:<line>: <reason>", the line left
// out when 0, and " (included from <file>:<line>)" after it for a refusal in a partial; or for a
// refusal of the rendered output "<file> (rendered for <target>): <reason>", "merged for" when
// the file is an attribute file, which is merged, never rendered.
func (e *TemplateError) text() string {
	switch {
	case e.rendered && e.merged:
		return fmt.Sprintf("%s (merged for %s): %s", e.File, e.Target, e.Reason)
	case e.rendered:
		return fmt.Sprintf("%s (rendered for %s): %s", e.File, e.Target, e.Reason)
	}
	return (&templating.Error{File: e.File, Line: e.Line, Reason: e.Reason, IncludedFrom: e.IncludedFrom}).Error()
}

// JSONFields is the template object of the refusal's --json document (Plan 7 §11.2); every
// string in it passed untrusted.Text or is lola's own.
func (e *TemplateError) JSONFields() map[string]any {
	return map[string]any{"template": map[string]any{"module": e.Module, "target": e.Target, "scope": e.Scope, "kind": e.Kind,
		"artifact": e.Artifact, "file": e.File, "line": e.Line, "code": e.Code, "reason": e.Reason, "included_from": e.IncludedFrom,
		"rendered": e.rendered}}
}

// checkHint is the lola check command that lists every template refusal of the module ent
// installs, at sc with the assistants the run was given (targets, its -t) and its output flags
// (display): its marketplace address, its local path, or its git URL when the entry gives no
// ref or path, which lola check cannot take; "" when no check reaches the version refused. A
// pinned entry gets one only when fresh, a fresh install, pinned it: lola check fetches the
// newest version, never a pin. A timeout or the run budget gets none: check refuses the same
// way. Nor does an address untrusted.Text would change, which would not name the version
// refused.
func (e *Engine) checkHint(sc Scope, err error, ent *manifest.Entry, targets, display []string, fresh bool) string {
	te, ok := errors.AsType[*TemplateError](err)
	switch {
	case !ok || te.Code == codeTemplateTimeout || te.Code == codeTemplateRunBudget:
		return ""
	case ent == nil: // a module no layer wants has no entry to name
		return ""
	case ent.Commit != "" && !fresh: // marketplace entries too
		return ""
	}
	addr := ""
	switch {
	case ent.Market != nil:
		addr = "@" + ent.Market.Name + "/" + ent.Name
	case ent.Components != nil: // a selection of a source, not through a marketplace, which no check repeats
		return ""
	case ent.Source == "": // an install by cache name records no source to check
	default:
		spec, isSrc, err := source.Parse(ent.Source, sourceRoot(sc))
		if err != nil || !isSrc || spec.Kind == source.KindGit && (ent.Ref != "" || ent.Path != "") {
			return ""
		}
		addr = spec.Location
	}
	if addr == "" || untrusted.Text(addr, untrusted.MaxName) != addr {
		return ""
	}
	args := []string{addr}
	for _, t := range targets {
		args = append(args, "--target", t)
	}
	return e.Command(sc, "check", append(args, display...)...)
}

// finding is the refusal as lola check reports it: a skip of its component for its target.
func (e *TemplateError) finding() target.Finding {
	return target.Finding{Severity: target.SeveritySkip, Module: e.Module, Target: e.Target, Kind: e.Kind, Artifact: e.Artifact, Code: e.Code, Text: e.text(),
		Hint: strings.Join(e.hints, "; ")}
}

// templateJob is one template of a module rendered for a target: the component it belongs to
// and its source; or, merged, the attribute file merged into the component.
type templateJob struct {
	kind, name string
	src        templating.Source
	merged     bool
}

// refusal is a TemplateError about j rendered for d, without its code and reason.
func (j templateJob) refusal(d templating.Data) *TemplateError {
	return &TemplateError{Module: untrusted.Text(j.src.Module, untrusted.MaxName), Target: d.Target, Scope: d.Scope, Kind: j.kind,
		Artifact: untrusted.Text(j.name, untrusted.MaxName), File: untrusted.Text(j.src.File, untrusted.MaxName), merged: j.merged}
}

// renderTemplates returns m rendered, merged and rechecked for one target and scope, m itself
// when it has no template and no attribute file. It never mutates m. A component template's
// frontmatter is parsed and may not rename the component; then the component's attributes for
// the target are merged in, and the load checks that read frontmatter run on the copy, consent
// gating rendered hooks and MCP servers later (Plan 7 §5.6, §5.7.2). The first refusal stops it.
func (e *Engine) renderTemplates(ctx context.Context, m *module.Module, d templating.Data, run *templateRun) (*module.Module, error) {
	out, _, err := e.renderAll(ctx, m, d, run, false)
	return out, err
}

// checkTemplates is renderTemplates for lola check (Plan 7 §9): each template refusal but a
// timeout or the run budget is returned in refused and leaves its component out of the copy,
// and rendering goes on, so the author sees every one at once.
func (e *Engine) checkTemplates(ctx context.Context, m *module.Module, d templating.Data, run *templateRun) (*module.Module, []*TemplateError, error) {
	return e.renderAll(ctx, m, d, run, true)
}

func (e *Engine) renderAll(ctx context.Context, m *module.Module, d templating.Data, run *templateRun, keepGoing bool) (*module.Module, []*TemplateError, error) {
	if !hasTemplates(m) && !hasAttributes(m) {
		return m, nil, nil
	}
	out := *m
	out.Skills, out.Commands, out.Agents, out.Rules = slices.Clone(m.Skills), slices.Clone(m.Commands), slices.Clone(m.Agents), slices.Clone(m.Rules)
	var refused []*TemplateError
	partials := templating.Partials{Files: m.Partials}
	declared := slices.Sorted(maps.Keys(m.Vars)) // a template may read only the vars m declares
	failed := map[string]bool{}                  // kind and name of each component left out
	// fail records a refusal of j: in check mode it leaves the component out and goes on (nil).
	fail := func(j templateJob, err error) error {
		te, ok := errors.AsType[*TemplateError](err)
		if se, isSandbox := errors.AsType[*templating.Error](err); isSandbox {
			te, ok = j.refusal(d), true
			te.Line, te.Code, te.Reason = se.Line, se.Code, se.Reason
			if se.IncludedFrom != "" { // a refusal in a partial names the partial
				te.File, te.IncludedFrom = se.File, se.IncludedFrom
			}
			err = te
		}
		if !ok || !keepGoing || te.Code == codeTemplateTimeout {
			return err // a TemplateError, maybe with hints, or the context's error
		}
		refused, failed[j.kind+" "+j.name] = append(refused, te), true
		return nil
	}
	// checked refuses j's rendered output when a load check that reads frontmatter refuses part,
	// a module holding only j's rendered component, or a rule's refusal is err.
	checked := func(j templateJob, part *module.Module, err error) error {
		if part != nil {
			part.Format = m.Format
			err = module.Recheck(part)
		}
		if err == nil {
			return nil
		}
		te := j.refusal(d)
		// the refusal names the file already; a rule's own message starts with it too
		reason := strings.TrimPrefix(err.Error(), j.src.File+": ")
		te.Code, te.Reason, te.rendered, te.hints = codeRenderedCheck, untrusted.Text(reason, 2*untrusted.MaxName), true, errs.Hints(err)
		return fail(j, errs.WithHints(te, te.hints...))
	}
	// doc renders j's component file into into, which a refusal in check mode leaves as it was;
	// the template sees the component's attributes for the target (attrs).
	doc := func(j templateJob, named bool, into *module.Doc, attrs map[string]any) error {
		d := d
		d.Attrs = attrs
		start := e.Now()
		r, err := templating.RenderDoc(ctx, j.src, partials, d, e.TemplateLimits, declared, module.PlainRead)
		if serr := e.spend(run, m.Name, d.Scope, start, len(r.Front)+len(r.Body)); serr != nil {
			return serr // a failed render took its time too
		}
		if err != nil {
			return fail(j, err)
		}
		if *into, err = renderedDoc(j, r, d, named); err != nil {
			return fail(j, err)
		}
		return nil
	}
	// merge merges a component's attributes for the target into into, refusing as a rendered
	// check does when they cannot be written; a literal component's job names its attribute file.
	merge := func(j templateJob, into *module.Doc, attrs map[string]any, template bool) error {
		merged, err := withAttributes(*into, attrs, template)
		if err != nil {
			return checked(j, nil, err)
		}
		*into = merged
		return nil
	}
	// component renders the template or merges the attributes of one skill, command or agent
	// file into into, then runs the load checks on part, which holds the component as into ends.
	// A refusal of keys the attribute file added to a template's output names the file: the
	// template's own output is checked first.
	component := func(kind, name string, into *module.Doc, attrs map[string]map[string]any, attrsFile string, part func() *module.Module) error {
		forTarget, template := attrs[d.Target], into.Template != nil
		attrsJob := templateJob{kind: kind, name: name, src: templating.Source{Module: m.Name, File: attrsFile}, merged: true}
		switch {
		case template:
			j := templateJob{kind: kind, name: name, src: componentSource(m, into.Template.File, into.Body)}
			if err := doc(j, true, into, attributesData(forTarget)); err != nil || failed[kind+" "+name] {
				return err
			}
			rendered := *into
			if err := merge(j, into, forTarget, true); err != nil || failed[kind+" "+name] {
				return err
			}
			if len(into.Frontmatter) == len(rendered.Frontmatter) { // the file added nothing
				return checked(j, part(), nil)
			}
			merged := *into
			*into = rendered
			if err := checked(j, part(), nil); err != nil || failed[kind+" "+name] {
				return err
			}
			*into = merged
			return checked(attrsJob, part(), nil)
		case forTarget != nil:
			if err := merge(attrsJob, into, forTarget, false); err != nil || failed[kind+" "+name] {
				return err
			}
			return checked(attrsJob, part(), nil)
		}
		return nil
	}
	for i := range out.Skills {
		s := &out.Skills[i]
		if err := component("skill", s.Name, &s.Doc, s.Attrs, s.AttrsFile, func() *module.Module { return &module.Module{Skills: []module.Skill{*s}} }); err != nil {
			return nil, nil, err
		}
		s.Files = slices.Clone(s.Files)
		for k := range s.Files {
			f := &s.Files[k]
			j := templateJob{kind: "skill", name: s.Name, src: templating.Source{Module: m.Name, File: path.Join(s.Dir, f.Source), Line: 1, Body: f.Body}}
			start := e.Now()
			fd := d
			fd.Attrs = attributesData(s.Attrs[d.Target])
			b, err := templating.Render(ctx, j.src, partials, fd, e.TemplateLimits, declared)
			if serr := e.spend(run, m.Name, d.Scope, start, len(b)); serr != nil {
				return nil, nil, serr
			}
			if err != nil {
				if err := fail(j, err); err != nil {
					return nil, nil, err
				}
				continue
			}
			f.Body = string(b)
		}
	}
	for i := range out.Commands {
		c := &out.Commands[i]
		if err := component("command", c.Name, &c.Doc, c.Attrs, c.AttrsFile, func() *module.Module { return &module.Module{Commands: []module.Command{{Name: c.Name, Doc: c.Doc}}} }); err != nil {
			return nil, nil, err
		}
		c.Sidecars = slices.Clone(c.Sidecars)
		for k := range c.Sidecars {
			sc := &c.Sidecars[k]
			if sc.Doc.Template == nil {
				continue
			}
			j := templateJob{kind: "command", name: c.Name, src: componentSource(m, sc.Source, sc.Doc.Body)}
			if err := doc(j, false, &sc.Doc, attributesData(c.Attrs[d.Target])); err != nil {
				return nil, nil, err
			}
			if !failed["command "+c.Name] {
				if err := checked(j, &module.Module{Commands: []module.Command{{Name: c.Name, Sidecars: []module.Sidecar{*sc}}}}, nil); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	for i := range out.Agents {
		a := &out.Agents[i]
		if err := component("agent", a.Name, &a.Doc, a.Attrs, a.AttrsFile, func() *module.Module { return &module.Module{Agents: []module.Agent{*a}} }); err != nil {
			return nil, nil, err
		}
	}
	for i := range out.Rules {
		r := &out.Rules[i]
		if r.Template == nil {
			continue
		}
		var rendered module.Doc
		j := templateJob{kind: "rule", name: r.Name, src: componentSource(m, r.Template.File, r.Body)}
		if err := doc(j, true, &rendered, map[string]any{}); err != nil {
			return nil, nil, err
		}
		if failed["rule "+r.Name] {
			continue
		}
		body, err := module.RenderedRule(r.Template.File, rendered)
		if err := checked(j, nil, err); err != nil {
			return nil, nil, err
		}
		r.Body, r.Template = body, nil
	}
	out.Skills = slices.DeleteFunc(out.Skills, func(s module.Skill) bool { return failed["skill "+s.Name] })
	out.Commands = slices.DeleteFunc(out.Commands, func(c module.Command) bool { return failed["command "+c.Name] })
	out.Agents = slices.DeleteFunc(out.Agents, func(a module.Agent) bool { return failed["agent "+a.Name] })
	out.Rules = slices.DeleteFunc(out.Rules, func(r module.Rule) bool { return failed["rule "+r.Name] })
	return &out, refused, nil
}

// componentSource is the Source of a component's template file.
func componentSource(m *module.Module, file, body string) templating.Source {
	return templating.Source{Module: m.Name, File: file, Line: 1, Body: body, Frontmatter: true}
}

// attributesData is a component's attributes for one target as its templates see them (attrs):
// {} when it has none.
func attributesData(attrs map[string]any) map[string]any {
	if attrs == nil {
		return map[string]any{}
	}
	return attrs
}

// withAttributes merges a component's attributes for one target into doc (Plan 7 §3.4), one
// level deep: into a literal component each key replaces the frontmatter key of its name whole,
// keeping comments and the order of the others, and null removes it; into a rendered template,
// whose frontmatter wins, only the keys it lacks are added, and null removes nothing.
func withAttributes(doc module.Doc, attrs map[string]any, template bool) (module.Doc, error) {
	set, drop := map[string]any{}, []string{}
	for k, v := range attrs {
		_, has := doc.Frontmatter[k]
		switch {
		case template && (has || v == nil):
		case v == nil:
			drop = append(drop, k)
		default:
			set[k] = v
		}
	}
	doc, err := doc.With(set)
	if err != nil {
		return module.Doc{}, err
	}
	return doc.Without(drop...)
}

// hasAttributes reports whether a skill, command or agent of m has an attribute file.
func hasAttributes(m *module.Module) bool {
	return slices.ContainsFunc(m.Skills, func(s module.Skill) bool { return s.Attrs != nil }) ||
		slices.ContainsFunc(m.Commands, func(c module.Command) bool { return c.Attrs != nil }) ||
		slices.ContainsFunc(m.Agents, func(a module.Agent) bool { return a.Attrs != nil })
}

// renderedDoc assembles a rendered component from its regions, then refuses a frontmatter that
// does not parse or that a rendered --- or ... line would end, and, for a named component, a
// name: other than its own (Plan 7 §5.6, §5.7.2).
func renderedDoc(j templateJob, r templating.Rendered, d templating.Data, named bool) (module.Doc, error) {
	refuse := func(code, reason string) error {
		te := j.refusal(d)
		te.Code, te.Reason, te.rendered = code, reason, true
		return te
	}
	doc, err := module.RenderedDoc(r.Front, r.HasFront, r.Body)
	switch {
	case errors.Is(err, module.ErrRenderedFence):
		return module.Doc{}, refuse(codeTemplateFence, err.Error())
	case err != nil:
		return module.Doc{}, refuse(codeTemplateFrontmatter, err.Error())
	}
	if v, ok := doc.Frontmatter["name"]; ok && named {
		name, text := v.(string)
		switch {
		case !text:
			return module.Doc{}, refuse(codeTemplateName, "name must be text; it is "+valueKind(v))
		case name != j.name:
			return module.Doc{}, refuse(codeTemplateName, fmt.Sprintf("renders name: %s, but its path names it %s",
				untrusted.Text(name, untrusted.MaxName), untrusted.Text(j.name, untrusted.MaxName)))
		}
	}
	return doc, nil
}

// valueKind names the kind of a frontmatter value in a refusal, in the sandbox's words.
func valueKind(v any) string {
	switch v.(type) {
	case string:
		return "a string"
	case bool:
		return "true or false"
	case int, int64, uint64, float64:
		return "a number"
	case map[string]any:
		return "a map"
	case []any:
		return "a list"
	case nil:
		return "nil"
	}
	return "a value of an unsupported kind"
}

// hasTemplates reports whether any component of m, a skill's support file or a sidecar is a
// template.
func hasTemplates(m *module.Module) bool { return len(templateSources(m)) > 0 }

// templateSources lists the source of every template of m.
func templateSources(m *module.Module) []templating.Source {
	var out []templating.Source
	for _, t := range m.Templates() {
		out = append(out, templating.Source{Module: m.Name, File: t.File, Line: 1, Body: t.Body})
	}
	return out
}

// renderedWith is what m's templates render with in run under an entry that overrides vars,
// which lock.json records (Plan 7 §6.3): nil for a module without templates; the active profiles
// only when a template reads them, so a switch re-renders only the modules it changes.
func renderedWith(m *module.Module, run *templateRun, vars map[string]any) *ledger.RenderedWith {
	srcs := templateSources(m)
	if len(srcs) == 0 {
		return nil
	}
	rw := &ledger.RenderedWith{Vars: manifest.CloneVars(vars)}
	if rw.Vars == nil {
		rw.Vars = map[string]any{}
	}
	if templating.Uses(srcs, templating.Partials{Files: m.Partials}, "profiles") {
		rw.Profiles = slices.Clone(run.profiles)
	}
	return rw
}

// sameRendering reports whether two records of what a module rendered with are the same.
func sameRendering(a, b *ledger.RenderedWith) bool {
	if a == nil || b == nil {
		return a == b
	}
	return (a.Profiles == nil) == (b.Profiles == nil) && slices.Equal(a.Profiles, b.Profiles) && manifest.SameVars(a.Vars, b.Vars)
}

// rerenders lists the installed modules at sc whose templates would render with other inputs
// under the active list: their recorded rendered_with differs from what they would render with
// now, read from the version installed, or, when the cache no longer holds it, records profiles
// other than the list's. Any other error reading the cache is returned. load reads a cached
// version (e.Store.Load).
func (e *Engine) rerenders(sc Scope, led *ledger.Ledger, list []ledger.Active, load func(name, id string) (store.Version, error)) ([]string, error) {
	run, err := e.newTemplateRun(sc, list)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, name := range sortedNames(led) {
		in := led.Modules[name]
		id, ok := strings.CutPrefix(in.Digest, "sha256:")
		var v store.Version
		if ok && len(id) >= 12 {
			v, err = load(name, id[:12])
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, err // a version the cache holds but cannot read is not a version it lost
			}
		}
		switch {
		case ok && len(id) >= 12 && err == nil:
			var vars map[string]any // what it rendered with, which a switch does not change
			if in.RenderedWith != nil {
				vars = in.RenderedWith.Vars
			}
			if !sameRendering(in.RenderedWith, renderedWith(v.Module, run, vars)) {
				out = append(out, name)
			}
		case in.RenderedWith != nil && in.RenderedWith.Profiles != nil && !slices.Equal(in.RenderedWith.Profiles, run.profiles):
			out = append(out, name)
		}
	}
	return out, nil
}

// templateRun is one plan pass's rendering (Plan 7 §6.2): the scope's active profiles, in
// activation order, which every template of the pass sees, and the budget the pass has spent:
// rendering time and rendered bytes held.
type templateRun struct {
	profiles []string
	spent    *runSpent // shared by the pass's runs under other profiles (under)
}

// runSpent is what a plan pass has spent of its budget.
type runSpent struct {
	time time.Duration
	held int
}

// under is the pass r rendering with other profiles, on r's budget: the version installed,
// which a trust diff renders with the profiles it was installed under.
func (r *templateRun) under(profiles []string) *templateRun {
	if r.spent == nil {
		r.spent = &runSpent{}
	}
	return &templateRun{profiles: profiles, spent: r.spent}
}

// spend charges one render that started at start and returned n bytes to the pass, refusing
// the render that takes it past the engine's budget.
func (e *Engine) spend(r *templateRun, module string, sc string, start time.Time, n int) error {
	if r.spent == nil {
		r.spent = &runSpent{}
	}
	r.spent.time, r.spent.held = r.spent.time+e.Now().Sub(start), r.spent.held+n
	refuse := func(reason string) error {
		return &TemplateError{Module: untrusted.Text(module, untrusted.MaxName), Scope: sc, Code: codeTemplateRunBudget, Reason: reason}
	}
	switch b := e.TemplateBudget; {
	case r.spent.time > b.RunTime:
		return refuse(fmt.Sprintf("rendering templates took more than %s; lola stops after %s per run", b.RunTime, b.RunTime))
	case r.spent.held > b.RunOutput:
		return refuse(fmt.Sprintf("rendering templates held more than %s of rendered templates; at most %s per run", budgetSize(b.RunOutput), budgetSize(b.RunOutput)))
	}
	return nil
}

// budgetSize writes a byte count as the sandbox's messages do: "10 bytes" under 1 KiB, "32 MiB"
// for an exact MiB, else KiB rounded up.
func budgetSize(n int) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d bytes", n)
	case n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	}
	return fmt.Sprintf("%d KiB", (n+1<<10-1)>>10)
}

// newTemplateRun starts a plan pass at sc that renders with the active list: the qualified names
// lola profile lists as active, default first at user scope when its file exists (Plan 7 §3.2).
func (e *Engine) newTemplateRun(sc Scope, active []ledger.Active) (*templateRun, error) {
	r := &templateRun{profiles: []string{}}
	if sc.Kind == target.User {
		_, err := os.Stat(e.profilePath(sc, profile.KindDefault, profile.DefaultName))
		switch {
		case err == nil:
			r.profiles = append(r.profiles, profile.DefaultName)
		case !errors.Is(err, fs.ErrNotExist):
			return nil, err
		}
	}
	for _, a := range active {
		r.profiles = append(r.profiles, a.Name)
	}
	return r, nil
}

// resolvedVars is what m's templates see as vars when ment (nil for none) installs it (Plan 7
// §4.2): each var m declares, with the entry's value where the entry sets one.
func resolvedVars(m *module.Module, ment *manifest.Entry) map[string]any {
	out := maps.Clone(m.Vars)
	if out == nil {
		out = map[string]any{}
	}
	if ment != nil {
		maps.Copy(out, ment.Vars)
	}
	return out
}

// checkVars refuses an override in ment (nil for none), the entry from the layer from ("" for
// the scope's manifest), of a var m does not declare or of another type than its default (Plan 7
// §4.2), before anything renders. The hint shows m's vars with the run's output flags, when the
// cache's current version of m is the one planned, digest: lola cache info shows no other.
func (e *Engine) checkVars(sc Scope, m *module.Module, ment *manifest.Entry, from, digest string, display []string) error {
	if ment == nil {
		return nil
	}
	file := from
	if file == "" {
		file = filepath.Base(e.files(sc).manifest)
	}
	declared := slices.Sorted(maps.Keys(m.Vars))
	for _, k := range slices.Sorted(maps.Keys(ment.Vars)) {
		at := fmt.Sprintf("%s: modules[%s].vars.%s", file, shown(m.Name), shown(k))
		var err error // a varsRefusal's text
		def, ok := m.Vars[k]
		switch {
		case !ok && len(declared) == 0:
			err = fmt.Errorf("%s: %s declares no var %s; it declares none", at, shown(m.Name), shown(k))
		case !ok:
			err = fmt.Errorf("%s: %s declares no var %s; it declares %s", at, shown(m.Name), shown(k), strings.Join(declared, ", "))
		case varKind(def) != varKind(ment.Vars[k]) || varKind(ment.Vars[k]) == "unsupported":
			err = fmt.Errorf("%s: %s's default is %s; this is %s", at, shown(m.Name), varKind(def), varKind(ment.Vars[k]))
		default:
			continue
		}
		// A cache that cannot be read, or holds another version, has no command that shows these vars.
		if cur, _, cerr := e.Current(m.Name); cerr != nil || digest == "" || cur.Meta.Digest != digest {
			return &varsRefusal{err.Error()}
		}
		hint := errs.ShellJoin(append([]string{"lola", "cache", "info", untrusted.Text(m.Name, untrusted.MaxName)}, display...))
		return errs.WithHints(&varsRefusal{err.Error()}, hint) // the cache is not a scope's: no scope flags
	}
	return nil
}

// varsRefusal is checkVars' refusal, which names the module and its entry itself, so a trust
// diff passes it on as it is.
type varsRefusal struct{ text string }

func (r *varsRefusal) Error() string { return r.text }

// varKind names a var's type in a refusal: "unsupported" for anything an entry could not set,
// which checkVars refuses whatever the default is.
func varKind(v any) string {
	switch v.(type) {
	case string:
		return "a string"
	case bool:
		return "true or false"
	case int64, float64: // one kind: an integer default takes a float override and back
		return "a number"
	case []string:
		return "a list of strings"
	}
	return "unsupported"
}

// data is what the pass's templates of a module whose vars resolve to vars see for target at sc;
// renderAll binds each component's attributes.
func (r *templateRun) data(target string, sc Scope, vars map[string]any) templating.Data {
	if vars == nil {
		vars = map[string]any{}
	}
	return templating.Data{Target: target, Scope: sc.Kind.String(), Profiles: r.profiles, Vars: vars, Attrs: map[string]any{}}
}
