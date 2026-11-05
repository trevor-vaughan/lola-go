# Templates

Maintainer notes on Templates. It is split out of [architecture](../architecture.md).

A Markdown component named `<name>.md.liquid` is a template. `module.Load` finds it
wherever it finds the literal file (native layout, `components` entries, both format adapters),
reads it capped (256 per module, 256 KiB, UTF-8) and never parses it: `Doc.Template` is set and
`Doc.Body` holds the source; support files and sidecars record their source path. Load refuses a
literal file beside its template (names compared case-folded) and warns about case variants.

It
reads partials the same way, from regular files in `_partials/` at the module root only
(`loadPartials`, `internal/module/partials.go`: `<name>.liquid` or `<name>.<target>.liquid`, 256
files, 256 KiB each, 4 MiB together), into `Module.Partials`; other entries warn. A root skill
excludes `_partials` and its case variants, so partials never reach a target; only the root
entry named exactly `_partials` is read, so a case-folding file system cannot load `_Partials`,
and a component path under `_partials` is refused.

`planModule` and `Check` render between the resolver and the targets, once per target, into a copy
of the module (`renderTemplates`, `internal/engine/templates.go`), so no target ever sees a
template.

For each component template:

- `templating.RenderDoc` renders, with the module's partials, the frontmatter region, up to the first literal `---` line at block depth 0, and the body region as two templates of one file sharing its deadline, output cap and assign budget
- `module.RenderedDoc` parses the frontmatter with `ParseDoc`'s rules and refuses a rendered line that is `---` or `...` with only spaces, tabs or a carriage return after it (`module.ErrRenderedFence`)
- A `name:` other than the path's, or one that is not text, is refused
- Then `module.Recheck` reruns, on a module holding only that rendered component, the load checks that read frontmatter (hooks, an agent's MCP servers, sidecar hooks, an Agent Plugins skill's agentskills.io rules) and `module.RenderedRule` a rule's `paths:` refusal; either refusal is a `template-rendered-check` `TemplateError` with `rendered` set.

Between the name check and `Recheck`, the component's attribute file for the target is merged in
(`withAttributes`); a literal component with one gets the merge and `Recheck` alone, its refusal
naming the attribute file "(merged for <target>)" (JSON keeps `rendered`).

When the file adds keys to a template's output, `Recheck` runs on the template's output first, so a
refusal after the merge names the attribute file too. Targets then compute intents, findings and
consent on the rendered copy, so runtime-hook consent, keyed by a digest of the hooks as rendered,
asks again whenever a template renders them differently.

No value can break a line in frontmatter (below), so a value adds a key only where the template puts
it at a line start; there is no key guard: what such a key can execute, hooks
and MCP servers, is gated by consent under every spelling each target accepts, or dropped with
`frontmatter-key-dropped` where the assistant does not read it (a conformance spec walks the
registry), and any other key comes from the author's defaults and attribute files or the user's
vars, which trust covers.

Residual: a capability key (`permissionMode`, `allowed-tools`, `tools`, OpenCode's `permission`) is
reachable that way, bounded by trust, plus a warning on Claude Code.

**Frontmatter values.** Before compiling a region or a partial, `withValueCheck` appends ` |
lola_frontmatter_value` to the markup of every `{{ }}` outside `raw` and `comment` that is its
line's whole value or whole key and whose last filter is not `json` (`wholeValue`, from the source
text around the token), and ` | lola_frontmatter_break` to every other, keeping the token's
delimiters, trims and whitespace (an object may span lines through the whitespace around its markup,
so rebuilding it would move lines); a lone `-` (`{{-}}`) stays markup.

The filters' closure knows the region: in the frontmatter both refuse a value when any string liquid
would write for it, through lists and maps and map keys included, holds a YAML line break (yaml.v3's
`is_break`: `\n`, `\r`, U+0085, U+2028, U+2029); in the body they return their input.

`json` escapes all five, so a json-ended value passes, and a filter after `json` is checked on its
own output. `lola_frontmatter_value` also refuses a string that YAML would not read back as itself
written as a plain block-context scalar: the engine passes `module.PlainRead`, which
parses `k: <value>` with yaml.v3, to `RenderDoc`, so the depguard-fenced templating package parses
no YAML.

Only a whole value or key is checked: a value composed with the author's text (`v{{ x }}`,
`"{{ x }}"`) is the author's to shape, so a ` #` there can still cut it short. Numbers and booleans
are not checked. The check is a data-correctness aid, not a security boundary: consent and the
recheck see the parsed result.

It also misses a `{{ }}` inside a tag on the same line (`{% if c %}d: {{ v }}{% endif %}`), one
followed by a comment (`# note`, a `{% comment %}` tag), one under a quoted or `?` key, a value a
partial included on the line writes, and a list or nil value (a list prints concatenated, nil
empty); the advice is `| json`.

Markup the appended filter leaves invalid refuses at compile. The refusal,
`template-frontmatter-value`, is mapped from the filter's error at the object's line: for a line
break quoting the markup when it is the line's only `{{ }}`, for a round trip naming the value and
what YAML would read.

A template refusal is an `engine.TemplateError` (module, target, scope, component, file, line, code,
reason, and for a refusal in a partial the partial's file and `IncludedFrom`, its first include
site): it refuses the run before anything is written, with a `lola check` hint where one reaches the
version refused (`checkHint`: never for a pin but a fresh install's, `Options.freshPin`, since check
fetches the newest version); `lola check` turns each into a `skip` finding and keeps going, except a
timeout or the run budget.

A plan pass (`templateRun`) holds the profiles every template sees (`default`, then the active
profile) and a budget of 20 s of rendering and 32 MiB rendered, charged for every render, failed or
not. sync's trust diff and its first pass share one; the pass after a partial trust answer starts a
fresh one; every profile one `lola profile --add` or `--update` diffs renders under one.

The diff renders the version installed with the profiles and vars `rendered_with` recorded, so a
changed list or vars edit shows in its hooks; without a valid record it renders the defaults, never
the new entry's vars. Residual: sync publishes a covered pinned entry (one the trust record covers,
as every entry at user scope is) to the cache before it plans, so a template refusal
leaves that cache head moved; nothing else changes and the run directory is removed.

A profile switch re-plans, besides the modules it moves, every installed module whose recorded
`rendered_with` differs from what the new profile renders with; `templating.Uses` follows includes
into the partials of every target, so a module that reads `profiles` only in a partial counts.

**The sandbox.** `internal/templating` builds a fresh `render.Config` per render with the standard
tags and lola's own filters, never liquid's, and strict variables.

A screen over liquid's token scanner refuses before compiling what liquid would accept but lola does not:

- Denied and unknown tags (`capture`, `tablerow`, `render`)
- An `include` of anything but a quoted partial name
- `..` outside string literals in markup (ranges are Go structs whose methods liquid calls; strings are stripped exactly as liquid's expression scanner reads them, `scanner.rl`, and an unterminated quote leaves the rest outside, so a lexer difference fails closed; a spec compares the two on a corpus in every position a range can take)
- More than 256 `assign`s, blocks or parentheses nested over 64
- Undeclared `vars` names
- `vars` or `attrs` printed whole
- `forloop` used other than through a property or a quoted literal key
- Rebinding `forloop`, `vars` or `attrs`.

Bindings are plain maps (as sorted `IterationKeyedMap`s), lists, strings, booleans and numbers
(`int64`, finite `float64`), so no Go method is reachable. `attrs` is bound as a `values.Value`
(`lenientValue`): liquid's `ValueOf` returns a `Value` as it is, so templates reach it only through
its methods, and a key or index it lacks, at any depth of a path, reads as an `absent` value (nil,
false, equal only to nil) instead of liquid's `undefined`, which strict variables refuse;
`Interface` hands `for`, filters and `json` the plain sorted maps, so an alias reads strictly again.

`vars` stays strict, its names checked before rendering. `lola check` warns (`attrs-undefined`,
`engine.attrsUndefined`) on each literal `attrs` path no attribute file of the module defines for
any target; a property of a scalar under `attrs` reads as absent too, but `size`, `first` and `last`
keep liquid's meaning.

The math filters (`plus` … `abs`) follow liquid's numeric semantics on `int64` and `float64` and
refuse an `int64` overflow, a non-finite result and a zero divisor, where liquid would wrap, print
NaN or coerce a string; `divided_by` divides two integers in `int64`, exactly, where liquid goes
through a float. Every growing filter checks its result against 64 KiB and 4096 items before
allocating; output is capped at 1 MiB per file and each render runs under its own deadline in a
recovering goroutine.

Memory is so bounded by construction: values arise only from capped literals, bindings and filter
results and outlive their expression only through `assign`. A loop that neither writes nor filters
cannot be interrupted; Render returns at the deadline and the refused run ends the process. Residual
risks: an engine-internal growth path not found in the review; a non-yielding loop's CPU until exit;
a map nested in a list prints in Go syntax.

**Partials.** Because include names are literals, `screen` (`internal/templating/graph.go`) builds
each template's include graph for the target before compiling: it resolves each name to
`_partials/<name>.<target>.liquid`, else `<name>.liquid`, screens every partial reached once, and
refuses a cycle, a chain of more than 8 partials (`Limits.Chain`), and more than 256 `assign`s or 64
open blocks across the template and its partials.

A name with no partial for the target is a missing leaf, with no file and no edges, so it counts in
none of these; the store refuses it (`template-include-missing`, at the include tag) only when a
rendering include asks for it, so an include under a false `{% if %}` needs no partial.

A partial the frontmatter includes gets the frontmatter's value check, and a `---` or `...` line it
writes is left to the rendered check. The render's `TemplateStore` (`store`) serves only the graph's
partials, under exactly the paths liquid asks for: the template's directory joined with the name
(liquid compiles a partial under that path, so a nested include asks in the same directory).

Every read counts and the 65th refuses (`Limits.Reads`); no error wraps `fs.ErrNotExist`, so
liquid's own cache is never consulted. An error liquid reports under such a path is mapped back to
the partial's file and line, with its first include site in graph (depth-first) order as
`IncludedFrom`: liquid does not report which site was rendering. A partial inherits the includer's
bindings; its own `assign`s stay in its scope.

**The vendored liquid.** `osteele/liquid` v1.9.2 is vendored in `third_party/liquid` with one
patch (`PATCHES.md`: chained right-trims flush liquid's trim buffer), under a `replace`.
`govulncheck` therefore reports the module as `./third_party/liquid` with no version and cannot
match advisories for `github.com/osteele/liquid` to it: those advisories are checked by hand
whenever the copy is touched and before each lola release ([release.md](../release.md), item 3).
