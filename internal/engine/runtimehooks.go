package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Runtime hook consent statuses.
const (
	RuntimeHooksConsented = "consented" // recorded earlier, or granted in this run
	RuntimeHooksDeclined  = "declined"  // not installed; any installed earlier are removed
)

// promptCut is how much of a prompt or agent hook's text consent shows (design §7.3).
const promptCut = 200

// RuntimeHook is one runtime hook handler as consent shows it: every string comes from the
// module, so each is passed through untrusted.Text. Command is the command line as rendered,
// after the variable rewrite.
type RuntimeHook struct {
	Target         string   `json:"target"`
	Component      string   `json:"component,omitempty"`  // "<kind> <name>" for hooks or MCP servers in a component's frontmatter
	MCPServer      string   `json:"mcp_server,omitempty"` // an agent's inline MCP server, which has no event; Type is its transport
	Event          string   `json:"event"`
	Matcher        string   `json:"matcher,omitempty"`
	Type           string   `json:"type"`
	If             string   `json:"if,omitempty"`
	Async          bool     `json:"async,omitempty"`
	Shell          string   `json:"shell,omitempty"`
	Command        string   `json:"command,omitempty"`          // command
	URL            string   `json:"url,omitempty"`              // http: where it sends data
	Headers        []string `json:"headers,omitempty"`          // http: "Name: value", sorted
	AllowedEnvVars []string `json:"allowed_env_vars,omitempty"` // http: environment it sends
	Env            []string `json:"env,omitempty"`              // MCP server: "NAME=value", sorted
	Server         string   `json:"server,omitempty"`           // mcp_tool
	Tool           string   `json:"tool,omitempty"`             // mcp_tool
	Prompt         string   `json:"prompt,omitempty"`           // prompt and agent, cut at promptCut characters
	Fields         string   `json:"fields,omitempty"`           // every other field, as JSON
	GroupFields    string   `json:"group_fields,omitempty"`     // the matcher group's fields other than matcher and hooks, as JSON
	Status         string   `json:"status"`
	settings       string   // the settings file, root-relative, whose consent covers the hook
}

// consentRuntimeHooks decides, per module and settings file, whether the intents that install
// runtime hooks may be written (design §7.3). Every intent of kind target.KindRuntimeHook must
// carry a plan.HookConsent; one without is refused. Consent is recorded under
// "<module>/runtime-hooks/<sha256 of the settings file's resolved absolute path>" for a digest
// of the rendered hooks and the module version, so a new version, a changed rendering, another
// project or the other scope asks again. A recorded consent for the digest grants it;
// otherwise a --trust covering runtime hooks or a yes from AskRuntimeHooks (runtimeHookConsent). A dry run
// decides the same way but never asks or records. Without consent those intents leave the plan, so hooks installed under an
// earlier consent are removed (fail closed), with a runtime-hooks-declined warning, whose hint
// grants unasked too, the other kinds the run would be refused for (unaskedKinds).
func (e *Engine) consentRuntimeHooks(sc Scope, changes []plan.ModuleChange, opts Options, unasked TrustKinds, res *Result) error {
	type dest struct{ target, settings string }
	for i := range changes {
		c := &changes[i]
		var order []dest
		gated := map[dest][]plan.Intent{}
		for _, in := range c.Intents {
			if in.Consent == nil {
				hooks, err := e.writesClaudeHooks(sc, in)
				if err != nil {
					return err
				}
				if hooks {
					return fmt.Errorf("%s: %s planned runtime hooks in %s without a consent destination", c.Name, in.Target, in.Path)
				}
			}
			if in.Consent == nil {
				continue
			}
			d := dest{in.Target, in.Consent.Settings}
			if _, ok := gated[d]; !ok {
				order = append(order, d)
			}
			gated[d] = append(gated[d], in)
		}
		for _, d := range order {
			key, err := e.consentKey(sc, c.Name, d.target, d.settings)
			if err != nil {
				return err
			}
			hooks, digest, err := describeRuntimeHooks(gated[d], c.Entry.Digest)
			if err != nil {
				return fmt.Errorf("%s: runtime hooks: %w", c.Name, err)
			}
			status, err := e.runtimeHookConsent(key, c.Name, d.settings, digest, hooks, opts, res)
			if err != nil {
				return err
			}
			for j := range hooks {
				hooks[j].Status, hooks[j].settings = status, d.settings
			}
			if mr := slices.IndexFunc(res.Modules, func(m ModuleResult) bool { return m.Name == c.Name }); mr >= 0 {
				res.Modules[mr].RuntimeHooks = append(res.Modules[mr].RuntimeHooks, hooks...)
			}
			if status != RuntimeHooksDeclined {
				continue
			}
			c.Intents = slices.DeleteFunc(c.Intents, func(in plan.Intent) bool {
				return in.Consent != nil && in.Target == d.target && in.Consent.Settings == d.settings
			})
			res.Findings = append(res.Findings, declinedFindings(c.Name, gated[d], opts, unasked)...)
		}
	}
	return nil
}

// writesClaudeHooks reports whether an intent installs runtime hooks: it has the runtime hook
// kind, or, whatever its kind, it edits /hooks in a Claude Code settings file (settings.json
// or settings.local.json in a .claude directory, ignoring case for case-insensitive file
// systems). The consent gate refuses either without a consent destination, so no target can
// reach Claude Code's hooks around it.
func (e *Engine) writesClaudeHooks(sc Scope, in plan.Intent) (bool, error) {
	if in.Kind == target.KindRuntimeHook {
		return true, nil
	}
	if in.Pointer != "/hooks" && !strings.HasPrefix(in.Pointer, "/hooks/") {
		return false, nil
	}
	t, err := e.target(in.Target)
	if err != nil {
		return false, err
	}
	abs := filepath.Join(t.Root(sc.Kind, sc.ProjectDir, e.Paths), filepath.FromSlash(in.Path))
	base := filepath.Base(abs)
	return strings.EqualFold(filepath.Base(filepath.Dir(abs)), ".claude") &&
		(strings.EqualFold(base, "settings.json") || strings.EqualFold(base, "settings.local.json")), nil
}

// declinedFindings reports what declined consent leaves out: a target's settings hook groups,
// and each component that carries hooks. The hint is the run with --trust covering runtime hooks
// too, and unasked (opts.Rerun), or, without one, the flags to add.
func declinedFindings(module string, gated []plan.Intent, opts Options, unasked TrustKinds) []target.Finding {
	consent := "rerun with " + (TrustRuntimeHooks | unasked).Arg()
	if opts.Rerun != nil {
		consent = "consent to them: " + opts.Rerun(opts.Trust|TrustRuntimeHooks|unasked)
	}
	var out []target.Finding
	seen := map[string]bool{}
	for _, in := range gated {
		kind, name := in.Consent.Kind, in.Consent.Name
		if kind == "" {
			kind = target.KindRuntimeHook
		}
		id := in.Target + "\x00" + kind + "\x00" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		text := "runtime hooks skipped: they run inside " + in.Target + " with your permissions, and lola installs them only with your consent"
		hint := "review the hooks listed above, then " + consent
		if name != "" {
			var what []string
			if in.Consent.Hooks != nil {
				what = append(what, "frontmatter hooks")
			}
			if in.Consent.MCPServers != nil {
				what = append(what, "inline MCP servers")
				hint = "review the hooks and MCP servers listed above, then " + consent
			}
			text = "skipped: its " + strings.Join(what, " and ") + " run inside " + in.Target + " with your permissions, and lola installs them only with your consent"
		}
		out = append(out, target.Finding{Severity: target.SeverityWarn, Module: module, Target: in.Target, Kind: kind, Artifact: name,
			Code: in.Target + "/runtime-hooks-declined", Text: text, Hint: hint})
	}
	return out
}

// consentKey is the consent.json key of module's runtime hooks in the settings file at
// settings (root-relative) for targetID: the file's absolute path with symlinks resolved,
// hashed, so one rule covers both scopes and every project.
func (e *Engine) consentKey(sc Scope, module, targetID, settings string) (string, error) {
	t, err := e.target(targetID)
	if err != nil {
		return "", err
	}
	r, err := fsx.Resolve(t.Root(sc.Kind, sc.ProjectDir, e.Paths), settings)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(r.Abs))
	return module + "/runtime-hooks/" + hex.EncodeToString(sum[:]), nil
}

// runtimeHookConsent returns the consent status of module's runtime hooks with digest in the
// settings file at settings (root-relative), keyed by key: a recorded consent grants it; a --trust
// covering runtime hooks grants it, added to res.Trusted and recorded only after the receipt
// (R-9-3, A2); in a batch's apply pass (opts.Expect), the grant for key and digest the batch
// holds, a terminal yes of its collect pass, grants it and is recorded the same way; otherwise
// AskRuntimeHooks may ask, and a yes is recorded with them (A23). A dry run
// decides like a real run but never asks or records.
func (e *Engine) runtimeHookConsent(key, module, settings, digest string, hooks []RuntimeHook, opts Options, res *Result) (string, error) {
	ok, err := e.Consent.Granted(key, digest)
	switch {
	case err != nil:
		return "", err
	case ok:
		return RuntimeHooksConsented, nil
	case opts.Trust.Covers(TrustRuntimeHooks):
		change, err := e.consentChange(key)
		if err != nil {
			return "", err
		}
		res.cover(TrustRuntimeHooks, shown(module)+" in "+shown(settings), change, &grant{key: key, digest: digest})
		return RuntimeHooksConsented, nil
	case opts.Expect != nil && slices.Contains(opts.Expect.grants, grant{key: key, digest: digest}):
		res.grants = append(res.grants, grant{key: key, digest: digest})
		return RuntimeHooksConsented, nil
	case opts.DryRun || opts.AskRuntimeHooks == nil:
		return RuntimeHooksDeclined, nil
	}
	yes, err := opts.AskRuntimeHooks(module, slices.Clone(hooks))
	if err != nil || !yes {
		return RuntimeHooksDeclined, err
	}
	res.grants = append(res.grants, grant{key: key, digest: digest})
	return RuntimeHooksConsented, nil
}

// describeRuntimeHooks lists the handlers of the hooks gated intents install, for consent:
// settings hook groups, and the frontmatter hooks and inline MCP servers of each component
// (once, though several of its intents carry them). It returns the digest consent is keyed by:
// sha256 over each group's target, pointer and canonical JSON, each component's target, kind,
// name and canonical hooks and servers, all as rendered, and the content digest of the module
// version (design §7.3).
func describeRuntimeHooks(gated []plan.Intent, versionDigest string) ([]RuntimeHook, string, error) {
	all := sha256.New()
	var out []RuntimeHook
	seen := map[string]bool{}
	for _, in := range gated {
		if in.Consent.Kind == "" {
			canon, err := fsx.CanonicalJSON(in.Member)
			if err != nil {
				return nil, "", err
			}
			fmt.Fprintf(all, "%s\x00%s\x00%s\n", in.Target, in.Pointer, canon)
			views, err := describeGroup(canon, in.Target, strings.TrimPrefix(in.Pointer, "/hooks/"), "")
			if err != nil {
				return nil, "", err
			}
			out = append(out, views...)
			continue
		}
		id := in.Target + "\x00" + in.Consent.Kind + "\x00" + in.Consent.Name
		if seen[id] {
			continue
		}
		seen[id] = true
		component := in.Consent.Kind + " " + in.Consent.Name
		if in.Consent.Hooks != nil {
			canon, err := fsx.CanonicalJSON(in.Consent.Hooks)
			if err != nil {
				return nil, "", err
			}
			fmt.Fprintf(all, "%s\x00%s\x00%s\n", id, "frontmatter", canon)
			var events map[string][]json.RawMessage
			if err := json.Unmarshal(canon, &events); err != nil {
				return nil, "", err
			}
			for _, event := range slices.Sorted(maps.Keys(events)) {
				for _, g := range events[event] {
					views, err := describeGroup(g, in.Target, event, component)
					if err != nil {
						return nil, "", err
					}
					out = append(out, views...)
				}
			}
		}
		if in.Consent.MCPServers != nil {
			canon, err := fsx.CanonicalJSON(in.Consent.MCPServers)
			if err != nil {
				return nil, "", err
			}
			fmt.Fprintf(all, "%s\x00%s\x00%s\n", id, "mcpServers", canon)
			views, err := describeServers(canon, in.Target, component)
			if err != nil {
				return nil, "", err
			}
			out = append(out, views...)
		}
	}
	fmt.Fprintf(all, "%s\n", versionDigest)
	return out, "sha256:" + hex.EncodeToString(all.Sum(nil)), nil
}

// describeGroup is the consent view of one matcher group's handlers. Fields are read by their
// exact names, as Claude Code reads them; the group's other fields (kept as authored, since
// Claude Code may add some) are shown with every handler.
func describeGroup(raw []byte, targetID, event, component string) ([]RuntimeHook, error) {
	var group map[string]json.RawMessage
	if err := json.Unmarshal(raw, &group); err != nil {
		return nil, err
	}
	var matcher string
	var handlers []map[string]json.RawMessage
	if m, ok := group["matcher"]; ok {
		if err := json.Unmarshal(m, &matcher); err != nil {
			return nil, err
		}
	}
	if err := json.Unmarshal(group["hooks"], &handlers); err != nil {
		return nil, err
	}
	rest := maps.Clone(group)
	delete(rest, "matcher")
	delete(rest, "hooks")
	extra, err := fieldsJSON(rest)
	if err != nil {
		return nil, err
	}
	var out []RuntimeHook
	for _, h := range handlers {
		view, err := describeHandler(h)
		if err != nil {
			return nil, err
		}
		view.Target, view.Component, view.Event, view.Matcher, view.GroupFields = targetID, shown(component), shown(event), shown(matcher), extra
		out = append(out, view)
	}
	return out, nil
}

// fieldsJSON is the consent view of the fields in rest that it shows nowhere else: readable
// JSON, or "" when there are none.
func fieldsJSON(rest map[string]json.RawMessage) (string, error) {
	if len(rest) == 0 {
		return "", nil
	}
	b, err := json.Marshal(rest)
	if err == nil {
		b, err = fsx.ReadableJSON(b)
	}
	return shown(string(b)), err
}

// describeServers is the consent view of an agent's inline MCP servers, canon being their
// canonical JSON list of single-server objects. Fields are read by their exact names, as
// Claude Code reads them; env and header values are shown, since they can change what runs
// (LD_PRELOAD) or where data goes, and every other field is shown as JSON.
func describeServers(canon []byte, targetID, component string) ([]RuntimeHook, error) {
	var items []map[string]map[string]json.RawMessage
	if err := json.Unmarshal(canon, &items); err != nil {
		return nil, err
	}
	var out []RuntimeHook
	for _, item := range items {
		for _, name := range slices.Sorted(maps.Keys(item)) {
			s := item[name]
			var errs []error
			get := func(key string, v any) {
				if raw, ok := s[key]; ok {
					errs = append(errs, json.Unmarshal(raw, v))
				}
			}
			var typ, command, url string
			var args []string
			var env, headers map[string]string
			get("type", &typ)
			get("command", &command)
			get("args", &args)
			get("env", &env)
			get("url", &url)
			get("headers", &headers)
			switch {
			case typ != "":
			case command != "":
				typ = "stdio"
			case url != "":
				typ = "http"
			}
			v := RuntimeHook{Target: targetID, Component: shown(component), MCPServer: shown(name), Type: shown(typ), URL: shown(url)}
			if command != "" || len(args) > 0 {
				words := []string{word(command)}
				for _, a := range args {
					words = append(words, word(a))
				}
				v.Command = shown(strings.Join(words, " "))
			}
			for _, k := range slices.Sorted(maps.Keys(env)) {
				v.Env = append(v.Env, shown(k+"="+env[k]))
			}
			for _, k := range slices.Sorted(maps.Keys(headers)) {
				v.Headers = append(v.Headers, shown(k+": "+headers[k]))
			}
			rest := maps.Clone(s)
			for _, k := range []string{"type", "command", "args", "env", "url", "headers"} {
				delete(rest, k)
			}
			fields, err := fieldsJSON(rest)
			v.Fields = fields
			if err = errors.Join(append(errs, err)...); err != nil {
				return nil, err
			}
			out = append(out, v)
		}
	}
	return out, nil
}

// shown is module text for consent: control characters replaced, nothing cut, since consent
// must show what runs. The module's JSON files are capped at 1 MiB.
func shown(s string) string { return untrusted.Text(s, len(s)) }

// describedFields are the handler fields the consent view shows in their own place, by type;
// every other field is shown as JSON, so nothing a handler carries is hidden.
var describedFields = map[string][]string{
	"command":  {"command", "args"},
	"http":     {"url", "headers", "allowedEnvVars"},
	"mcp_tool": {"server", "tool"},
	"prompt":   {"prompt"},
	"agent":    {"prompt"},
}

// describeHandler is one handler's consent view, without its target, event and matcher. Fields
// are read by their exact names, as Claude Code reads them.
func describeHandler(h map[string]json.RawMessage) (RuntimeHook, error) {
	var errs []error
	get := func(key string, v any) {
		if raw, ok := h[key]; ok {
			errs = append(errs, json.Unmarshal(raw, v))
		}
	}
	var typ, ifRule, shell, command, url, server, tool, prompt string
	var async bool
	var args *[]string
	var headers map[string]string
	var envVars []string
	get("type", &typ)
	get("if", &ifRule)
	get("async", &async)
	get("shell", &shell)
	v := RuntimeHook{}
	switch typ {
	case "command":
		get("command", &command)
		get("args", &args)
		v.Command = shown(command)
		if args != nil {
			words := []string{word(command)}
			for _, a := range *args {
				words = append(words, word(a))
			}
			v.Command = shown(strings.Join(words, " "))
		}
	case "http":
		get("url", &url)
		get("headers", &headers)
		get("allowedEnvVars", &envVars)
		v.URL = shown(url)
		for _, name := range slices.Sorted(maps.Keys(headers)) {
			v.Headers = append(v.Headers, shown(name+": "+headers[name]))
		}
		for _, name := range envVars {
			v.AllowedEnvVars = append(v.AllowedEnvVars, shown(name))
		}
	case "mcp_tool":
		get("server", &server)
		get("tool", &tool)
		v.Server, v.Tool = shown(server), shown(tool)
	case "prompt", "agent":
		get("prompt", &prompt)
		v.Prompt = cut(prompt)
	}
	rest := maps.Clone(h)
	for _, k := range append([]string{"type", "if", "async", "shell"}, describedFields[typ]...) {
		delete(rest, k)
	}
	fields, err := fieldsJSON(rest)
	errs = append(errs, err)
	v.Fields = fields
	v.Type, v.If, v.Async, v.Shell = shown(typ), shown(ifRule), async, shown(shell)
	return v, errors.Join(errs...)
}

// plainWord is an exec-form argument consent can show without quotes.
var plainWord = regexp.MustCompile(`^[A-Za-z0-9/._+=:,@%-]+$`)

// word is one exec-form argument as consent shows it: quoted unless plain, so the arguments'
// boundaries stay visible.
func word(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return strconv.Quote(s)
}

// cut is prompt text for consent: at most promptCut characters, marked "(cut)" when longer.
func cut(s string) string {
	if utf8.RuneCountInString(s) <= promptCut {
		return shown(s)
	}
	n := 0
	for i := range s {
		if n == promptCut {
			return shown(s[:i]) + " (cut)"
		}
		n++
	}
	return shown(s)
}

// driftFindings reports runtime hook groups lola installed that are no longer in their
// settings file as it wrote them: the user edited or removed them, and since a group is
// identified by its content the two look the same. Render and install leave such a group
// absent and still owned (Plan 5b design §14: writing it again would run both copies) and
// report it as info with a hint, since it records the user's choice rather than something lola
// lost: --strict does not escalate it, and human output prints it because of its hint, restore,
// the run with --force that writes it again. Uninstall, or a declined update, forgets it and
// leaves the file alone; its hint says the edited group may still run files from the module
// copy under modules.
func driftFindings(steps []plan.Step, modules, restore string) []target.Finding {
	var out []target.Finding
	for _, st := range steps {
		if st.Kind != target.KindRuntimeHook {
			continue
		}
		event, path := untrusted.Text(strings.TrimPrefix(st.Key, "/hooks/"), untrusted.MaxName), untrusted.Text(st.Path, untrusted.MaxName)
		f := target.Finding{Module: st.Module, Target: st.Target, Kind: target.KindRuntimeHook, Code: st.Target + "/runtime-hook-drift"}
		switch st.Action {
		case plan.ActionAbsent:
			f.Severity, f.Hint = target.SeverityInfo, "restore it: "+restore
			f.Text = fmt.Sprintf("a %s hook group lola installed in %s is missing (edited or removed); lola leaves it out", event, path)
		case plan.ActionForget:
			f.Severity = target.SeverityInfo
			f.Hint = fmt.Sprintf("your edited group may still run files from the module copy at %s; remove it from %s if you do not trust that copy",
				untrusted.Text(filepath.Join(modules, st.Module), untrusted.MaxName), path)
			f.Text = fmt.Sprintf("a %s hook group lola installed in %s was edited or removed; lola left the file's hooks alone", event, path)
		default:
			continue
		}
		out = append(out, f)
	}
	return out
}
