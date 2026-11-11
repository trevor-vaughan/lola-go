package targetspec

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/mod/semver"
)

// validator checks one decoded spec, resolves its eras and compiles its patterns; file names
// the spec in errors.
type validator struct{ file string }

func (v validator) fail(field, problem, fix string) error {
	return &Error{File: v.file, Field: field, Problem: problem, Fix: fix}
}

// maxSummary is the longest an era's summary may be: lola shows it beside the era's label on one
// line of the version question.
const maxSummary = 80

var (
	idRE    = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	varRef  = regexp.MustCompile(`\{([a-z_]+)\}`)
	varName = regexp.MustCompile(`^[a-z_]+$`)
)

// mechRule is what a mechanism places and which Entry fields (by yaml name) it takes.
type mechRule struct{ kinds, required, allowed []string }

func mechanism(name string) (mechRule, bool) {
	switch name {
	case "skill-dir":
		return mechRule{[]string{"skill"}, []string{"path"}, []string{"path", "shared", "reads_shared_skills", "name_matches_dir", "prepend_name", "hook_settings"}}, true
	case "agentskills-dir":
		return mechRule{[]string{"skill"}, []string{"path"}, []string{"path", "shared"}}, true
	case "markdown-file":
		return mechRule{[]string{"command", "agent"}, []string{"path", "ext"}, []string{"path", "ext", "prepend_name", "defaults", "arguments", "sidecar", "hook_settings"}}, true
	case "toml-command":
		return mechRule{[]string{"command"}, []string{"path", "ext"}, []string{"path", "ext", "sidecar"}}, true
	case "json-servers":
		return mechRule{[]string{"mcp"}, []string{"file", "pointer", "shape"}, []string{"file", "pointer", "shape"}}, true
	case "rules-dir":
		return mechRule{[]string{"rule"}, []string{"path"}, []string{"path", "concat", "register"}}, true
	case "claude-hooks":
		return mechRule{[]string{"runtime-hook"}, []string{"file"}, []string{"file"}}, true
	}
	return mechRule{}, false
}

// Mechanisms are the mechanism names a layout entry may use.
func Mechanisms() []string {
	return []string{"agentskills-dir", "claude-hooks", "json-servers", "markdown-file", "rules-dir", "skill-dir", "toml-command"}
}

// set lists the yaml names of e's fields that are set, besides mech.
func (e Entry) set() []string {
	var out []string
	add := func(name string, on bool) {
		if on {
			out = append(out, name)
		}
	}
	add("path", e.Path != "")
	add("ext", e.Ext != "")
	add("shared", e.Shared)
	add("file", e.File.set())
	add("pointer", e.Pointer != "")
	add("shape", e.Shape != "")
	add("register", e.Register != nil)
	add("concat", e.Concat != "")
	add("reads_shared_skills", e.ReadsSharedSkills)
	add("name_matches_dir", e.NameMatchesDir)
	add("prepend_name", e.PrependName)
	add("defaults", len(e.Defaults) > 0)
	add("arguments", e.Arguments != "")
	add("sidecar", e.Sidecar != "")
	add("hook_settings", e.HookSettings != "")
	return out
}

func (v validator) spec(s *Spec) error {
	if !idRE.MatchString(s.ID) {
		return v.fail("id", fmt.Sprintf("%q is not a target id", s.ID), "use lowercase letters, digits and single inner hyphens, e.g. my-tool")
	}
	if err := v.probes(s); err != nil {
		return err
	}
	if err := v.roots(s.Roots); err != nil {
		return err
	}
	if err := v.detect("detect.project", s.Detect.Project); err != nil {
		return err
	}
	if err := v.detect("detect.user", s.Detect.User); err != nil {
		return err
	}
	if len(s.Eras) == 0 {
		return v.fail("eras", "is empty", "add an era with since: and layout:")
	}
	for i := range s.Eras {
		field, e := fmt.Sprintf("eras[%d]", i), s.Eras[i]
		if Canonical(e.Since) == "" {
			return v.fail(field+".since", fmt.Sprintf("%q is not a version", e.Since), "write a version such as 1.0 or 2.1.3")
		}
		if utf8.RuneCountInString(e.Summary) > maxSummary {
			return v.fail(field+".summary", fmt.Sprintf("is longer than %d characters", maxSummary), "say in a few words what this era reads differently")
		}
		if len(e.Remove) > 0 && e.Extends == "" {
			return v.fail(field+".remove", "deletes from the previous era, so it needs extends: previous", "add extends: previous, or remove remove:")
		}
		switch {
		case e.Extends != "" && e.Extends != "previous":
			return v.fail(field+".extends", fmt.Sprintf("%q is not previous", e.Extends), "write extends: previous, or remove it")
		case i == 0 && e.Extends != "":
			return v.fail(field+".extends", "the first era has no previous era", "remove extends: from the first era")
		case i > 0 && semver.Compare(Canonical(e.Since), Canonical(s.Eras[i-1].Since)) <= 0:
			return v.fail(field+".since", "since not after previous era", "order eras by increasing since:")
		case e.Extends != "":
			r, err := v.extend(field, s.Eras[i-1], e)
			if err != nil {
				return err
			}
			e = r
		}
		if err := v.era(field, &e); err != nil {
			return err
		}
		s.Eras[i] = e
	}
	return nil
}

func (v validator) probes(s *Spec) error {
	hasExec := false
	for i, p := range s.Probe {
		field := fmt.Sprintf("probe[%d]", i)
		var err error
		switch {
		case p.Exec == nil && p.File == nil:
			err = v.fail(field, "has no method", "give it exec: or file:")
		case p.Exec != nil && p.File != nil:
			err = v.fail(field, "has two methods", "split it into two probe entries")
		case p.Exec != nil:
			hasExec = true
			err = v.execProbe(field+".exec", p.Exec)
		default:
			err = v.fileProbe(field+".file", p.File)
		}
		if err != nil {
			return err
		}
	}
	switch {
	case hasExec && s.Readers != "":
		return v.fail("readers", "is only for a target with no exec probe", "remove readers:")
	case !hasExec && s.Readers == "":
		return v.fail("readers", "is empty for a target with no exec probe", "say what reads the files it writes, e.g. skills for Codex and Amp")
	}
	return nil
}

func (v validator) execProbe(field string, x *Exec) error {
	if len(x.Binaries) == 0 {
		return v.fail(field+".binaries", "is empty", "name the command looked up on PATH")
	}
	for i, b := range x.Binaries {
		if b == "" || strings.ContainsAny(b, `/\`) {
			return v.fail(fmt.Sprintf("%s.binaries[%d]", field, i), fmt.Sprintf("%q is not a command name", b), "give the name looked up on PATH, e.g. mytool")
		}
	}
	return v.versionMatch(field+".match", x.Match, "match the version, e.g. '(\\d+\\.\\d+\\.\\d+)'")
}

func (v validator) fileProbe(field string, f *FileProbe) error {
	if f.Path == "" {
		return v.fail(field+".path", "is empty", "name the file, e.g. {home}/.mytool/version.json")
	}
	if !probePath(f.Path) {
		return v.fail(field+".path", fmt.Sprintf("%q does not start with {home}/, {config_home}/ or {data_home}/ and a clean relative path", f.Path), "start it with one of those variables")
	}
	switch {
	case f.Match != nil && f.Pointer != "":
		return v.fail(field, "sets both match and pointer", "keep one")
	case f.Match == nil && f.Pointer == "":
		return v.fail(field, "has neither match nor pointer", "add match: '(\\d+\\.\\d+\\.\\d+)' or pointer: /version")
	case f.Pointer != "":
		if !jsonPointer(f.Pointer) {
			return v.fail(field+".pointer", fmt.Sprintf("%q is not a JSON pointer", f.Pointer), "write it as /key/sub, escaping ~ as ~0 and / as ~1")
		}
		return nil
	}
	return v.versionMatch(field+".match", f.Match, "")
}

// versionMatch compiles m and requires a group for the version; missing is the fix for a nil m,
// or "" when the caller has already checked it is set.
func (v validator) versionMatch(field string, m *Pattern, missing string) error {
	if m == nil {
		return v.fail(field, "is missing", missing)
	}
	if err := v.compile(field, m); err != nil {
		return err
	}
	if m.re.NumSubexp() < 1 {
		return v.fail(field, "has no group for the version", "capture the version, e.g. '(\\d+\\.\\d+\\.\\d+)'")
	}
	return nil
}

// probePath reports a probe file's path: a root variable, a slash and a clean relative path.
func probePath(p string) bool {
	for _, root := range []string{"{home}/", "{config_home}/", "{data_home}/"} {
		if rest, ok := strings.CutPrefix(p, root); ok {
			return relPath(rest) && !strings.ContainsAny(rest, "{}")
		}
	}
	return false
}

// jsonPointer reports an RFC 6901 pointer: it starts with / and every ~ is ~0 or ~1.
func jsonPointer(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] == '~' && (i+1 == len(p) || (p[i+1] != '0' && p[i+1] != '1')) {
			return false
		}
	}
	return true
}

func (v validator) compile(field string, p *Pattern) error {
	re, err := regexp.Compile(p.Source)
	if err != nil {
		return v.fail(field, "is not a valid regular expression: "+err.Error(), "fix the pattern (Go RE2 syntax)")
	}
	p.re = re
	return nil
}

func (v validator) roots(r Scoped) error {
	for _, x := range []struct {
		field, tmpl string
		vars        map[string]string
	}{
		{"roots.project", r.Project, map[string]string{"project": "/project"}},
		{"roots.user", r.User, map[string]string{"home": "/home", "config_home": "/config", "data_home": "/data"}},
	} {
		if bad := unknownVar(x.tmpl, x.vars); bad != "" {
			return v.fail(x.field, bad+" is not allowed here", "roots.project uses {project}; roots.user uses {home}, {config_home} or {data_home}")
		}
		loc := varRef.FindStringIndex(x.tmpl)
		if loc == nil || loc[0] != 0 || !rootRest(x.tmpl[loc[1]:]) {
			return v.fail(x.field, fmt.Sprintf("%q is not a clean path under a root variable", x.tmpl), "write the variable alone, or the variable, a slash and clean relative parts, e.g. {config_home}/mytool")
		}
	}
	return nil
}

func (v validator) detect(field string, d Detect) error {
	switch {
	case (len(d.Any) > 0) == (d.Dir != ""):
		return v.fail(field, "needs exactly one of any: or dir:", "e.g. any: [.mytool/, MYTOOL.md] or dir: .")
	case d.Dir == ".":
		return nil
	case d.Dir != "" && !relPath(d.Dir):
		return v.fail(field+".dir", fmt.Sprintf("%q escapes the target root or is not a clean relative path", d.Dir), "write . or a path relative to the root")
	}
	for i, m := range d.Any {
		if !relPath(strings.TrimSuffix(m, "/")) {
			return v.fail(fmt.Sprintf("%s.any[%d]", field, i), fmt.Sprintf("%q escapes the target root or is not a clean relative path", m), "write a path relative to the root, e.g. .mytool/")
		}
	}
	return nil
}

// relPath reports a clean, relative, slash-separated path with no .. element.
func relPath(p string) bool {
	return p != "" && p != "." && !strings.HasPrefix(p, "/") && !strings.ContainsAny(p, `\:`) &&
		path.Clean(p) == p && !slices.Contains(strings.Split(p, "/"), "..")
}

// rootRest reports whether what follows a root template's variable is empty or a slash and a
// clean relative path with no variable.
func rootRest(rest string) bool {
	return rest == "" || (strings.HasPrefix(rest, "/") && relPath(rest[1:]) && !strings.ContainsAny(rest, "{}"))
}

// unknownVar is the first {name} in t that vars lacks, "a stray { or }", or "".
func unknownVar(t string, vars map[string]string) string {
	for _, m := range varRef.FindAllStringSubmatch(t, -1) {
		if _, ok := vars[m[1]]; !ok {
			return m[0]
		}
	}
	if strings.ContainsAny(varRef.ReplaceAllString(t, ""), "{}") {
		return "a stray { or }"
	}
	return ""
}

// subst replaces each {name} in t with its value in vars.
func subst(t string, vars map[string]string) string {
	return varRef.ReplaceAllStringFunc(t, func(m string) string { return vars[m[1:len(m)-1]] })
}

func reserved(name string) bool {
	return slices.Contains([]string{"project", "home", "config_home", "data_home"}, name)
}

func (v validator) era(field string, e *Era) error {
	for _, name := range slices.Sorted(maps.Keys(e.Vars)) {
		val := e.Vars[name]
		if !varName.MatchString(name) || reserved(name) {
			return v.fail(field+".vars."+name, "is not a variable name lola allows", "use lowercase letters and _, other than project, home, config_home and data_home")
		}
		if strings.ContainsAny(val.Project+val.User, "{}") {
			return v.fail(field+".vars."+name, "may not hold {…}", "write the value out")
		}
	}
	if len(e.Entries) == 0 {
		return v.fail(field+".layout", "is empty", "place at least one kind, e.g. skill: {mech: skill-dir, path: .mytool/skills}")
	}
	e.project, e.user = map[string]Entry{}, map[string]Entry{}
	for _, kind := range slices.Sorted(maps.Keys(e.Entries)) {
		f := field + ".layout." + kind
		if !slices.Contains(Kinds(), kind) {
			return v.fail(f, "is not an artifact kind", "use one of "+strings.Join(Kinds(), ", "))
		}
		if err := v.entry(f, kind, e.Entries[kind]); err != nil {
			return err
		}
		for _, user := range []bool{false, true} {
			vars := map[string]string{}
			for n, s := range e.Vars {
				vars[n] = s.at(user)
			}
			x, err := v.expand(f, e.Entries[kind], vars)
			if err != nil {
				return err
			}
			if user {
				e.user[kind] = x
			} else {
				e.project[kind] = x
			}
		}
	}
	if err := v.hookFile(field, e); err != nil {
		return err
	}
	for _, kind := range slices.Sorted(maps.Keys(e.Kinds)) {
		if err := v.kind(field+".kinds."+kind, kind, e); err != nil {
			return err
		}
	}
	if err := v.behaviors(field, e); err != nil {
		return err
	}
	for i, s := range e.Shared {
		f := fmt.Sprintf("%s.shared[%d]", field, i)
		if s != "skill-rules" {
			return v.fail(f, "unknown fragment "+s, "use skill-rules")
		}
		if _, ok := e.Entries["skill"]; !ok {
			return v.fail(f, "skill-rules describes skills, which the era's layout does not place", "place skills, or remove it")
		}
	}
	return nil
}

func in(s string, set ...string) bool { return slices.Contains(set, s) }

func (v validator) entry(f, kind string, e Entry) error {
	m, ok := mechanism(e.Mech)
	if !ok {
		return v.fail(f+".mech", "unknown mechanism "+e.Mech, "use one of "+strings.Join(Mechanisms(), ", "))
	}
	if !slices.Contains(m.kinds, kind) {
		return v.fail(f+".mech", e.Mech+" does not place "+kind+"s", "use a mechanism that places "+kind+"s")
	}
	if err := v.fileRef(f+".file", e.File); err != nil {
		return err
	}
	if e.Register != nil {
		if err := v.fileRef(f+".register.file", e.Register.File); err != nil {
			return err
		}
	}
	set := e.set()
	for _, name := range set {
		if !slices.Contains(m.allowed, name) {
			return v.fail(f+"."+name, "is not a field of "+e.Mech, "remove it")
		}
	}
	for _, name := range m.required {
		if !slices.Contains(set, name) {
			return v.fail(f+"."+name, "is missing", "set "+name+": for "+e.Mech)
		}
	}
	commandFile := kind == "command" && in(e.Mech, "markdown-file", "toml-command")
	switch {
	case commandFile && !in(e.Sidecar, "copy", "drop"):
		return v.fail(f+".sidecar", "must be copy or drop", "say whether the tool gets a command's sidecar directory")
	case !commandFile && e.Sidecar != "":
		return v.fail(f+".sidecar", "is only for commands", "remove it")
	case e.Arguments != "" && (kind != "command" || !in(e.Arguments, "keep", "one-based")):
		return v.fail(f+".arguments", "must be keep or one-based, on a command", "fix or remove it")
	case e.Shape != "" && !in(e.Shape, "claude", "gemini", "opencode"):
		return v.fail(f+".shape", "unknown server shape "+e.Shape, "use claude, gemini or opencode")
	case e.Pointer != "" && !strings.HasPrefix(e.Pointer, "/"):
		return v.fail(f+".pointer", "is not a JSON Pointer", "start it with /, e.g. /mcpServers")
	case e.Ext != "" && (!strings.HasPrefix(e.Ext, ".") || strings.ContainsAny(e.Ext, `/\`)):
		return v.fail(f+".ext", fmt.Sprintf("%q is not a file extension", e.Ext), "write it with its dot, e.g. .md")
	case e.Concat != "" && (strings.ContainsAny(e.Concat, `/\:`) || in(e.Concat, ".", "..")):
		return v.fail(f+".concat", fmt.Sprintf("%q is not a file name", e.Concat), "name one file, e.g. GEMINI.md")
	}
	if err := v.hookSettings(f, kind, e); err != nil {
		return err
	}
	if r := e.Register; r != nil {
		switch {
		case !r.File.set():
			return v.fail(f+".register.file", "is missing", "name the JSON file that lists what was written")
		case !strings.HasPrefix(r.List, "/"):
			return v.fail(f+".register.list", "is not a JSON Pointer", "start it with /, e.g. /instructions")
		case !in(r.Member, "dir", "glob"):
			return v.fail(f+".register.member", "must be dir or glob", "dir lists the module's directory; glob lists <dir>/*.md")
		}
	}
	return nil
}

// fileRef rejects a file: that is not a path or a list of two or more candidates.
func (v validator) fileRef(f string, r FileRef) error {
	switch {
	case r.bad == "first: is empty":
		return v.fail(f, "first: is empty", "list the candidate files, or write file: <path>")
	case r.bad != "":
		return v.fail(f, r.bad, "write a path, e.g. file: mytool.json")
	case len(r.First) == 1:
		return v.fail(f, "first: lists one candidate, so there is nothing to choose between", "write file: "+r.First[0])
	}
	return nil
}

// hookSettings rejects what e sets that lola ignores when it renders as Claude Code reads the
// component: an entry with hook_settings, and a claude-hooks file that is not one path.
func (v validator) hookSettings(f, kind string, e Entry) error {
	if e.Mech == "claude-hooks" && len(e.File.First) > 1 {
		return v.fail(f+".file", "must be a plain path, not a list of candidates",
			"name one settings file: file: "+e.File.First[len(e.File.First)-1])
	}
	if e.HookSettings == "" {
		return nil
	}
	for _, x := range []struct {
		name string
		on   bool
	}{
		{"arguments", e.Arguments != ""},
		{"defaults", len(e.Defaults) > 0},
		{"sidecar", e.Sidecar == "drop"},
		{"name_matches_dir", e.NameMatchesDir},
		{"prepend_name", e.PrependName && (e.Mech == "skill-dir" || e.Mech == "markdown-file" && kind == "command")},
	} {
		if x.on {
			return v.fail(f+"."+x.name, "is not honoured with hook_settings", fmt.Sprintf(
				"remove %s: with hook_settings lola renders the %s as Claude Code reads it, which ignores %s", x.name, kind, x.name))
		}
	}
	return nil
}

// hookFile checks, at both scopes, that each kind carrying hook_settings names the file of the
// era's claude-hooks runtime-hook, the one file consent for a component's hooks covers.
func (v validator) hookFile(field string, e *Era) error {
	for _, kind := range slices.Sorted(maps.Keys(e.Entries)) {
		if e.Entries[kind].HookSettings == "" {
			continue
		}
		f := field + ".layout." + kind + ".hook_settings"
		for _, layout := range []map[string]Entry{e.project, e.user} {
			rh, ok := layout["runtime-hook"]
			switch {
			case !ok || rh.Mech != "claude-hooks":
				return v.fail(f, "names a settings file, but the era has no claude-hooks runtime-hook",
					"add runtime-hook: {mech: claude-hooks, file: "+e.Entries[kind].HookSettings+"}, or remove hook_settings")
			case layout[kind].HookSettings != rh.File.Path:
				return v.fail(f, fmt.Sprintf("%s is not the runtime-hook file %s", layout[kind].HookSettings, rh.File.Path),
					fmt.Sprintf("set hook_settings to the runtime-hook file, %s: consent for a component's hooks covers that file", rh.File.Path))
			}
		}
	}
	return nil
}

// expand returns e with its path templates expanded with vars, each checked to be a clean path
// inside the target root. f is e's field path, for errors.
func (v validator) expand(f string, e Entry, vars map[string]string) (Entry, error) {
	one := func(field string, p *string) error {
		if *p == "" {
			return nil
		}
		if bad := unknownVar(*p, vars); bad != "" {
			return v.fail(f+"."+field, bad+" is not a variable of this era", "layout paths are relative to the target root and use only the era's vars")
		}
		x := subst(*p, vars)
		if !relPath(x) {
			return v.fail(f+"."+field, fmt.Sprintf("%q escapes the target root or is not a clean relative path", x), "write a path relative to the target root, without .. or a leading /")
		}
		*p = x
		return nil
	}
	ref := func(field string, r *FileRef) error {
		r.First = slices.Clone(r.First)
		if err := one(field, &r.Path); err != nil {
			return err
		}
		for i := range r.First {
			if err := one(fmt.Sprintf("%s.first[%d]", field, i), &r.First[i]); err != nil {
				return err
			}
		}
		return nil
	}
	if err := one("path", &e.Path); err != nil {
		return Entry{}, err
	}
	if err := one("hook_settings", &e.HookSettings); err != nil {
		return Entry{}, err
	}
	if err := ref("file", &e.File); err != nil {
		return Entry{}, err
	}
	if e.Register != nil {
		r := *e.Register
		if err := ref("register.file", &r.File); err != nil {
			return Entry{}, err
		}
		e.Register = &r
	}
	return e, nil
}

func (v validator) kind(f, kind string, e *Era) error {
	if _, ok := e.Entries[kind]; !ok {
		return v.fail(f, "describes a kind the era's layout does not place", "add layout."+kind+", or remove kinds."+kind)
	}
	k := e.Kinds[kind]
	if !in(k.UnknownKeys, "", "keep", "drop") {
		return v.fail(f+".unknown_keys", "must be keep or drop", "keep passes keys lola does not know; drop removes them with a warning")
	}
	if k.Name != nil {
		if err := v.compile(f+".name", k.Name); err != nil {
			return err
		}
	}
	for i, r := range k.Required {
		f := fmt.Sprintf("%s.required[%d]", f, i)
		if r == "" {
			return v.fail(f, "is empty", "remove the empty entry")
		}
		for name, key := range k.Keys {
			if key.Unsupported != "" && (name == r || key.CaseInsensitive && strings.EqualFold(name, r)) {
				return v.fail(f, r+" is required, but kinds."+kind+".keys."+name+" is also unsupported", "a key cannot be both required and unsupported; remove one")
			}
		}
	}
	var folded []string
	for _, name := range slices.Sorted(maps.Keys(k.Keys)) {
		key := k.Keys[name]
		if err := v.key(f+".keys."+name, key); err != nil {
			return err
		}
		if !key.CaseInsensitive {
			continue
		}
		if i := slices.IndexFunc(folded, func(o string) bool { return strings.EqualFold(o, name) }); i >= 0 {
			return v.fail(f+".keys."+name, "equals "+folded[i]+" ignoring case; both are case_insensitive", "make one of them case-sensitive, or merge them")
		}
		folded = append(folded, name)
	}
	return nil
}

func (v validator) key(f string, k Key) error {
	n := 0
	for _, on := range []bool{k.Accepts != nil, k.ImpliedBy != "", k.Unsupported != ""} {
		if on {
			n++
		}
	}
	if n != 1 {
		return v.fail(f, "needs exactly one of accepts:, implied_by: or unsupported:", "say what the tool does with the key")
	}
	if k.ImpliedBy != "" && k.ImpliedBy != "file-name" {
		return v.fail(f+".implied_by", "must be file-name", "write implied_by: file-name")
	}
	if a := k.Accepts; a != nil {
		if !in(a.Shape, "string", "string-list", "bool-map", "bool", "number", "positive-integer", "map") {
			return v.fail(f+".accepts.shape", fmt.Sprintf("%q is not a shape", a.Shape), "use string, string-list, bool-map, bool, number, positive-integer or map")
		}
		if a.Pattern != nil {
			if !in(a.Shape, "string", "string-list") {
				return v.fail(f+".accepts.pattern", "applies only to string and string-list", "remove it")
			}
			if err := v.compile(f+".accepts.pattern", a.Pattern); err != nil {
				return err
			}
		}
	}
	switch k.AbsentMeans {
	case "":
		if k.Alternative != "" || k.Example != "" {
			return v.fail(f, "alternative: and example: need absent_means: unrestricted", "add it, or remove them")
		}
	case "unrestricted":
		switch {
		case k.ImpliedBy != "":
			return v.fail(f+".absent_means", "does not go with implied_by:", "remove it")
		case (k.Alternative == "") == (k.Example == ""):
			return v.fail(f, "absent_means: unrestricted needs exactly one of alternative: or example:", "name the key the tool restricts with, or a value it accepts")
		case k.Example != "" && (k.Accepts == nil || k.Accepts.Shape != "string-list"):
			return v.fail(f+".example", "needs accepts: {shape: string-list}", "use alternative:, or describe the list the tool accepts")
		}
	default:
		return v.fail(f+".absent_means", "must be unrestricted", "write absent_means: unrestricted, or remove it")
	}
	return nil
}

func (v validator) behaviors(field string, e *Era) error {
	ids := map[string]bool{}
	for i, b := range e.Behaviors {
		f := fmt.Sprintf("%s.behaviors[%d]", field, i)
		switch {
		case !idRE.MatchString(b.ID):
			return v.fail(f+".id", fmt.Sprintf("%q is not an id", b.ID), "use lowercase letters, digits and single inner hyphens")
		case ids[b.ID]:
			return v.fail(f+".id", "repeats "+b.ID, "give each behavior its own id")
		case b.Kind != "command":
			return v.fail(f+".kind", "must be command", "behaviors describe command bodies")
		case b.Detect == nil:
			return v.fail(f+".detect", "is missing", "give the pattern that finds the construct")
		case b.What == "":
			return v.fail(f+".what", "is missing", "name the construct as the tool would, e.g. fenced shell")
		}
		ids[b.ID] = true
		if _, ok := e.Entries["command"]; !ok {
			return v.fail(f+".kind", "the era's layout does not place commands", "place commands, or remove the behavior")
		}
		if err := v.compile(f+".detect", b.Detect); err != nil {
			return err
		}
		switch b.Effect {
		case "sent-as-text":
			if (b.Native == "") == (b.Alternative == "") {
				return v.fail(f, "sent-as-text needs exactly one of native: or alternative:", "give the tool's own syntax (native:) or a form it runs (alternative:)")
			}
		case "runs-without-permission-check":
			if b.Native != "" || b.Alternative != "" {
				return v.fail(f, "native: and alternative: are for sent-as-text", "remove them")
			}
		default:
			return v.fail(f+".effect", "unknown effect "+b.Effect, "use sent-as-text or runs-without-permission-check")
		}
	}
	return nil
}

// Canonical turns "1.2" into the "v1.2" form semver compares (a leading v is kept), or "" when it is not a version.
func Canonical(s string) string {
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "v") {
		s = "v" + s
	}
	if !semver.IsValid(s) {
		return ""
	}
	return s
}
