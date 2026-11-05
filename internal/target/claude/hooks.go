package claude

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Runtime hook finding codes (Plan 5b design §7.2).
const (
	codePathUnsafe       = "claude-code/runtime-hook-path-unsafe"
	codeShellUnsupported = "claude-code/runtime-hook-shell-unsupported"
	codeNotIgnored       = "claude-code/settings-local-not-ignored"
	codeEventInvalid     = "claude-code/runtime-hook-event-invalid"
)

// notIgnoredHint is the coaching for settings.local.json that git could commit.
const notIgnoredHint = "add .claude/settings.local.json to .gitignore; Claude Code ignores it only when Claude Code creates it"

// hookEvent is a Claude Code hook event name. Anything else is no event Claude Code fires, and
// lola does not write it into the user's settings.
var hookEvent = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)

// shellSafe is a path the shell reads as one word, inside or outside quotes, with no expansion.
var shellSafe = regexp.MustCompile(`^[A-Za-z0-9/._+-]+$`)

// quoting are the characters that change how a POSIX shell reads quoted text. A forced render
// relies on single quotes and on variable references inside double quotes, so a path holding
// one of them is never forced into shell text.
const quoting = `'"$` + "`" + `\`

// pluginVar is a textual form of the plugin variables: ${CLAUDE_PLUGIN_ROOT}, ${PLUGIN_ROOT},
// $CLAUDE_PLUGIN_ROOT and their DATA forms. A bare form counts only when no identifier
// character follows it, which substitute checks.
var pluginVar = regexp.MustCompile(`\$\{(?:CLAUDE_)?PLUGIN_(ROOT|DATA)\}|\$CLAUDE_PLUGIN_(ROOT|DATA)`)

// hookVars are what the plugin variables' textual forms become in one handler.
type hookVars struct{ root, data string }

// substitute replaces the textual forms of the plugin variables in s, in one pass.
func (v hookVars) substitute(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range pluginVar.FindAllStringSubmatchIndex(s, -1) {
		name := ""
		if m[2] >= 0 {
			name = s[m[2]:m[3]]
		} else {
			if m[1] < len(s) && isIdent(s[m[1]]) {
				continue
			}
			name = s[m[4]:m[5]]
		}
		b.WriteString(s[last:m[0]])
		if name == "ROOT" {
			b.WriteString(v.root)
		} else {
			b.WriteString(v.data)
		}
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

func isIdent(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

// shellQuote is s in POSIX single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hookSet renders one runtime hook set into the settings file at path: each matcher group
// becomes one member of /hooks/<Event>. Anything that cannot be rendered safely skips the
// whole set (rewriteEvents).
func hookSet(id string, h module.RuntimeHookSet, ctx target.PlanContext, path string) ([]plan.Intent, []target.Finding, error) {
	var events map[string][]map[string]json.RawMessage
	if err := json.Unmarshal(h.Hooks, &events); err != nil {
		return nil, nil, fmt.Errorf("runtime hooks %s: %w", h.Name(), err)
	}
	sk := newSkipper(ctx, target.Finding{Target: id, Kind: target.KindRuntimeHook, Artifact: h.Name()})
	if ctx.Scope == target.Project && ctx.LocalSettingsExposed != "" {
		sk.skip(codeNotIgnored, ctx.LocalSettingsExposed, notIgnoredHint, true)
	}
	if err := rewriteEvents(events, ctx, sk); err != nil {
		return nil, nil, fmt.Errorf("runtime hooks %s: %w", h.Name(), err)
	}
	var out []plan.Intent
	for _, name := range slices.Sorted(maps.Keys(events)) {
		for _, group := range events[name] {
			member, err := json.Marshal(group)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, plan.Intent{Target: id, Kind: target.KindRuntimeHook, Op: plan.OpAddJSONMember, Path: path, Pointer: "/hooks/" + name, Member: member,
				Consent: &plan.HookConsent{Settings: path}})
		}
	}
	if !sk.render {
		return nil, sk.finds, nil
	}
	return out, sk.finds, nil
}

// skipper collects the findings of one artifact and whether it may still be rendered.
type skipper struct {
	ctx    target.PlanContext
	at     target.Finding
	finds  []target.Finding
	render bool
}

func newSkipper(ctx target.PlanContext, at target.Finding) *skipper {
	return &skipper{ctx: ctx, at: at, render: true}
}

func (s *skipper) skip(code, text, hint string, forceable bool) {
	f := s.at
	f.Code, f.Text, f.Hint, f.Forceable = code, text, hint, forceable
	f, r := target.Skipped(s.ctx, f)
	s.finds, s.render = append(s.finds, f), s.render && r
}

// rewriteEvents rewrites every handler of a hooks event map in place, giving command
// handlers CLAUDE_PLUGIN_ROOT and CLAUDE_PLUGIN_DATA, which Claude Code exports to plugin hooks
// but not to settings or component hooks (design §7.2), and reports to sk what it cannot render
// safely; a forced render is made so that no lola path is ever read as shell code. An event
// name that is not an identifier is removed and reported.
func rewriteEvents(events map[string][]map[string]json.RawMessage, ctx target.PlanContext, sk *skipper) error {
	safe := shellSafe.MatchString(ctx.PluginRoot) && shellSafe.MatchString(ctx.PluginData)
	shellForm := false
	for _, name := range slices.Sorted(maps.Keys(events)) {
		if !hookEvent.MatchString(name) {
			sk.skip(codeEventInvalid, fmt.Sprintf("%s is not a Claude Code hook event", untrusted.Text(strconv.Quote(name), untrusted.MaxName)),
				"name an event from Claude Code's hooks reference, such as PreToolUse or Stop", false)
			delete(events, name)
			continue
		}
		for i, group := range events[name] {
			var handlers []map[string]json.RawMessage
			if err := json.Unmarshal(group["hooks"], &handlers); err != nil {
				return fmt.Errorf("/%s/%d: %w", name, i, err)
			}
			for j, handler := range handlers {
				where := fmt.Sprintf("/%s/%d/hooks/%d", name, i, j)
				rendered, problem, err := commandHandler(handler, ctx, safe)
				if err != nil {
					return fmt.Errorf("%s: %w", where, err)
				}
				handlers[j] = rendered
				switch problem {
				case problemShell:
					shellForm = true
				case problemPowerShell:
					shellForm = true
					sk.skip(codeShellUnsupported, where+" runs in PowerShell, which cannot take the export lola adds for CLAUDE_PLUGIN_ROOT and CLAUDE_PLUGIN_DATA",
						"give the handler args (exec form), or let it run in bash", true)
				case problemExecUnsafe:
					sk.skip(codePathUnsafe, where+": env would read the exec-form command as an assignment or an option",
						"name the program by a path that does not start with - and has no =, such as ./bin/tool", true)
				}
			}
			b, err := json.Marshal(handlers)
			if err != nil {
				return err
			}
			group["hooks"] = b
		}
	}
	if shellForm && !safe {
		hint := "give the hooks args (exec form), which passes paths verbatim, or use paths of letters, digits and / . _ + - only"
		if strings.Contains(ctx.PluginRoot+ctx.PluginData, `\`) {
			hint = `give the hooks args (exec form), which passes paths verbatim: a shell-form hook never installs on a path with \, such as any Windows path`
		}
		sk.skip(codePathUnsafe, fmt.Sprintf("the module copy (%s) or its plugin data (%s) has a path a shell-form hook command cannot hold safely",
			untrusted.Text(ctx.PluginRoot, untrusted.MaxName), untrusted.Text(ctx.PluginData, untrusted.MaxName)),
			hint, !strings.ContainsAny(ctx.PluginRoot+ctx.PluginData, quoting))
	}
	return nil
}

// What commandHandler found in a handler.
const (
	problemNone       = iota
	problemShell      // shell form: needs shell-safe paths
	problemPowerShell // shell form run by PowerShell: no export
	problemExecUnsafe // exec form whose command env would misread
)

// commandHandler rewrites a command handler (any other type is returned as authored) and says
// what it needs or cannot have. safe says both paths are shell-safe.
//
//   - Shell form: the command is prefixed with an export of both variables, so every part of a
//     compound command sees them, and textual forms become the paths. When the paths are not
//     shell-safe (a forced skip, possible only for paths without a quoting character), the
//     export quotes them and the textual forms become variable references instead, so a POSIX
//     shell never reads a path as code.
//   - Shell form run by PowerShell: no export (a forced skip); textual forms become the paths
//     only when they are shell-safe.
//   - Exec form: command becomes env, with --, the variables, then the original command and
//     arguments, all verbatim. env takes -- only before the assignments (GNU env runs a -- that
//     follows them), and then reads the first word without = as the command, so a command with
//     = (or, for other env implementations, a leading -) is left unwrapped (a forced skip).
func commandHandler(h map[string]json.RawMessage, ctx target.PlanContext, safe bool) (map[string]json.RawMessage, int, error) {
	var typ, cmd, shell string
	if err := json.Unmarshal(h["type"], &typ); err != nil || typ != "command" {
		return h, problemNone, err
	}
	if err := json.Unmarshal(h["command"], &cmd); err != nil {
		return nil, problemNone, err
	}
	if raw, ok := h["shell"]; ok {
		if err := json.Unmarshal(raw, &shell); err != nil {
			return nil, problemNone, err
		}
	}
	paths := hookVars{root: ctx.PluginRoot, data: ctx.PluginData}
	out := maps.Clone(h)
	set := func(key string, v any) error {
		b, err := json.Marshal(v)
		out[key] = b
		return err
	}
	rawArgs, exec := h["args"]
	if exec {
		var args []string
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return nil, problemNone, err
		}
		cmd = paths.substitute(cmd)
		for i := range args {
			args[i] = paths.substitute(args[i])
		}
		if strings.Contains(cmd, "=") || strings.HasPrefix(cmd, "-") {
			if err := set("command", cmd); err != nil {
				return nil, problemNone, err
			}
			return out, problemExecUnsafe, set("args", args)
		}
		wrapped := append([]string{"--", "CLAUDE_PLUGIN_ROOT=" + ctx.PluginRoot, "CLAUDE_PLUGIN_DATA=" + ctx.PluginData, cmd}, args...)
		if err := set("command", "env"); err != nil {
			return nil, problemNone, err
		}
		return out, problemNone, set("args", wrapped)
	}
	if strings.EqualFold(shell, "powershell") {
		if safe {
			cmd = paths.substitute(cmd)
		}
		return out, problemPowerShell, set("command", cmd)
	}
	refs := hookVars{root: "${CLAUDE_PLUGIN_ROOT}", data: "${CLAUDE_PLUGIN_DATA}"}
	prefix := "export CLAUDE_PLUGIN_ROOT=" + shellQuote(ctx.PluginRoot) + " CLAUDE_PLUGIN_DATA=" + shellQuote(ctx.PluginData) + "; "
	if safe {
		cmd = paths.substitute(cmd)
	} else {
		cmd = refs.substitute(cmd)
	}
	return out, problemShell, set("command", prefix+cmd)
}

// componentHooks renders a skill, command or agent document whose frontmatter may carry hooks,
// which Claude Code runs while the component is active. Any key equal to "hooks" ignoring case
// counts. Without hooks the document is rendered as before: target.Expand over the whole
// text. With hooks, their command handlers get the settings-hook rewrite (rewriteEvents), and
// target.Expand reaches only the rest of the document, so it cannot put a lola path into a
// hook's shell text; the component's intents then carry a plan.HookConsent for settings, the
// settings file whose consent covers them. ok is false when the component is skipped.
func componentHooks(id, kind, name string, doc module.Doc, ctx target.PlanContext, settings string) (string, *plan.HookConsent, []target.Finding, bool, error) {
	var keys []string
	for k := range doc.Frontmatter {
		if strings.EqualFold(k, "hooks") {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return target.Expand(doc.Render(), ctx), nil, nil, true, nil
	}
	slices.Sort(keys)
	sk := newSkipper(ctx, target.Finding{Target: id, Kind: kind, Artifact: name})
	rest, err := doc.Without(keys...)
	if err != nil {
		return "", nil, nil, false, err
	}
	// Only the remaining frontmatter is parsed again; the body is never re-read, or a leading
	// "---" block in it would become frontmatter next to the consented hooks.
	expanded, err := module.ParseDoc([]byte(target.Expand(module.Doc{Frontmatter: rest.Frontmatter, RawFrontmatter: rest.RawFrontmatter}.Render(), ctx)))
	if err != nil {
		sk.skip("frontmatter-invalid", "the frontmatter does not parse once ${PLUGIN_ROOT} and ${PLUGIN_DATA} are expanded: "+
			untrusted.Text(err.Error(), untrusted.MaxName), "quote frontmatter values that hold these variables", false)
		return "", nil, sk.finds, false, nil
	}
	set := map[string]any{}
	merged := map[string][]json.RawMessage{}
	for _, k := range keys {
		raw, err := json.Marshal(doc.Frontmatter[k])
		if err != nil {
			return "", nil, nil, false, fmt.Errorf("%s %s: frontmatter %s: %w", kind, name, k, err)
		}
		var events map[string][]map[string]json.RawMessage
		if err := json.Unmarshal(raw, &events); err != nil {
			return "", nil, nil, false, fmt.Errorf("%s %s: frontmatter %s: %w", kind, name, k, err)
		}
		if err := rewriteEvents(events, ctx, sk); err != nil {
			return "", nil, nil, false, fmt.Errorf("%s %s: frontmatter %s: %w", kind, name, k, err)
		}
		b, err := json.Marshal(events)
		if err != nil {
			return "", nil, nil, false, err
		}
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			return "", nil, nil, false, err
		}
		set[k] = v
		for _, event := range slices.Sorted(maps.Keys(events)) {
			for _, g := range events[event] {
				gb, err := json.Marshal(g)
				if err != nil {
					return "", nil, nil, false, err
				}
				merged[event] = append(merged[event], gb)
			}
		}
	}
	if !sk.render {
		return "", nil, sk.finds, false, nil
	}
	final, err := expanded.With(set)
	if err != nil {
		return "", nil, nil, false, err
	}
	hooks, err := json.Marshal(merged)
	if err != nil {
		return "", nil, nil, false, err
	}
	final.Body = target.Expand(doc.Body, ctx)
	return final.Render(), &plan.HookConsent{Settings: settings, Kind: kind, Name: name, Hooks: hooks}, sk.finds, true, nil
}
