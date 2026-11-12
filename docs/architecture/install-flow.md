# Selections and the install flow

How selections work and how the install flow runs. It is split out of
[architecture](../architecture.md).

**Files.** `internal/config` reads `$XDG_CONFIG_HOME/lola/config.yaml` (`default_scope`, `pager`,
`pre_install`, `post_install`, `cache_unused_for`, `cache_fresh_for`) and `selections.yaml` once per
command. Both are untrusted: at most 1 MiB, one YAML document, no anchors, aliases or merge keys,
known keys only, `version: 1`, module names through `ValidateName`, known kinds, valid `path.Match`
globs (no control or format characters in a selector name, from the file or a flag), no `include` in
`global`, absolute project keys.

Errors name the file, line and key path (keys through `untrusted.Text`) with a hint. Project keys
pass through `paths.ProjectKey` (absolute, clean, symlinks resolved; on Windows forward slashes and
a lower drive letter) and compare with `paths.KeyID` (case-folded on darwin and Windows), so two
keys for one project are refused and a hand-written spelling still matches. The project lock file is
named after the same key.

In `selections.yaml` a project inside a git clone whose `origin` is on another host is keyed
by its repository instead: `config.RepoKey` runs `git rev-parse
--show-prefix` and `git config --get remote.origin.url` through `source.GitCommand` and writes
`git:<host>/<path>#<dir>`, the remote's host lowercased and its scheme, userinfo, default port,
query, trailing `/` and `.git` dropped (so no credential reaches the file; another port stays as
`<host>:<port>`), and the root's directory below the work-tree top (`.` at the top).

A local, `file://` or helper remote, or one holding a control or format character, keeps the path
key; so does a clone git cannot read, which `RepoKey` reports as an error with git's first line (and
the `safe.directory` command for a repository git refuses as someone else's) and `scopeSection`
keeps for the run (`keyFailure`): `lola info` shows it as a scope notice (`keyNotices`), `lola
check` as the `picks-keyed-by-path` finding; install, sync and status also add the notice, though
their tracked-lock check (`refuseTrackedLedger`) refuses such a clone first today.

A remote that would leave an `@` in the key (scp-style `user:token@host:path`) keeps the path key,
so no credential reaches the file; an IPv6 host is bracketed. Messages name a section by its key as
the file writes it (`Selections.Origin`, `WrittenKey`).

The engine resolves a scope's section once per run (`Engine.scopeSection`), as a `config.Where` that
carries both keys: a section under the path key still applies under the repository key's entries,
and the next write of one of its modules moves it to the repository key (renaming the key node in
place when the file has no section under the repository key), so the file's other entries stay
byte-stable. Repository keys compare as written and never match a path key.

**Resolver.** `resolveComponents` (engine) is the one place a module becomes the components a scope
installs: the cached version minus the effective exclusions and without rules when they are off.
`config.Exclusions` holds each place that sets selectors as a `Layer` with its origin
(`selections.yaml global`, `projects[<dir>]`, or `profile <name>`); its methods apply the
precedence: the scope's include installs a match, every layer's skip excludes, and the most specific
`only:` list (scope, else global) excludes what it misses.

A module a profile provides takes its picks from that profile entry's `only:` and `skip:`: a
`config.Picks(layer)` section, which `withPicks` fills from the entries of the stack's profile
layers (and the write target) as a copy of the loaded selections, parsed with `config.ParsePattern`;
the global `skip:` still applies to it, the global `only:` and a `user:` section do not
(`config.Load` refuses a `user:` section, naming the file and line).

`picksAt(sc, from)` names the section whose picks apply to a module `from` provides, and
`pickSection` the one an edit saves to: the entry of the profile the install writes, else
`selections.yaml`'s section of the scope (`project.yml`'s modules). `exclusions(sel, sc, from,
name)` is the one place the planners, `moduleScope` and `Choices.Preview` get them; `applyEdits`
builds the same layers from the entries it is editing, and `config.Save` refuses a change to a
profile or `user` section.

`Install`, `Sync` and `Check` plan its output, so a component skipped after installation is an
orphan `plan.Compute` removes (or a conflict, if hand-edited); `Status` reports the skipped names. A
pattern that names nothing is a warning notice.

**New components.** `installedSet` reads the components of the version a scope's `lock.json` entry
pins from the cache ("unknown" when the cache no longer holds it). `planInstallModule` and
`planSync` pass it, with the planned version and the exclusions, to `newComponentNotices`, which
names each component the planned version adds that only the `only:` list leaves out.

It returns before reading the cache when no `only:` list is in force. A sync's hint is `repickRun`'s
(target.go): the edit of the profile that provides the module (named by `editArg`), `lola install`
for `project.yml`'s while no profile is on (else `lola profile base --edit`), and for a remote
profile's the switch to a copy; an install's keeps its own target (`installRun`).

**Re-pick visibility.** `ignoredSection` (resolve.go) warns at plan time (sync, install) that a
`projects[<dir>]` entry is ignored for a module a profile layer provides. `originLabel` names a
selection layer as output does (a profile's by `layerLabel`). `moduleScope` splits `Excluded` into
`NotInOnly` and the skipped, and adds `New` (against `installedSet`) and `Provider`, which the
component page labels. `infoComponents` (info_components.go) gives `lola info` each scope's
provider, `componentStates` and `repickRun`, reading the layers under the shared profiles lock; a
scope where no layer provides the module has none. `overrideNotice` names dropped picks as it
names dropped vars.

**Mentions.** `scanMentions` (engine/mentions.go) reads a version's content directory through an
`os.Root` for the whole-token names of the module's other components (4 bytes or more; a skill's
files, a command's file and sidecars, an agent's file, a rule's body; symlinks, files over 1 MiB,
binary ones and everything past 16 MiB per module skipped; once the budget is spent no file is
opened) into `Mentions`; nothing is stored.

Names are found in one pass over the text by `nameMatcher` (engine/mention_match.go), an
Aho-Corasick automaton over the `.`-separated segments of the names, so the cost does not grow with
the number of components.

`Choices.Mentions` scans the version the choices list, staged or cached, for the component page's
`(mentions N)` and `Preview`; `InstallAll` reads the versions it resolved (`resolvedMentions`), and
`Info` the version it shows. A staged run keeps its scans (`StagedRun.scans`, by version ID), so an
interactive install reads each version once for its pickers, review and apply.

`editScope` is the one place install (`editSelections`) and `Choices.Preview` edit a scope: with
`InstallSpec.WithMentions` it first computes on a copy what the picks mention and leave out
(`mentionGaps`) and adds an `--unskip` for each (one hop); with `NameMentions`, set by `lola
install` only, `mentionNotice` names what is still left out with the install that adds it.

**Destinations.** An install acts on an ordered list of destinations: at most one user scope and any
number of project roots. `Scope.Key()` names one: `user`, or the project's canonical key
(`paths.ProjectKey`, compared through `paths.KeyID`), so every spelling of a root is one
destination.

Everything the flow keeps per destination is keyed by it: `ModuleScope.Scope` (one per destination
in `ModuleChoice.PerScope`), `Preview.Selected`, the flow's assistants, component picks and
dropped-pick counts. Each project root is its own `projects:` section of `selections.yaml`. Results
carry `scope` and `root` on `scopes`, modules and steps. The scope picker's **Other directory…**
adds a project root; the flags still give at most one project and the user scope.

**Scope.** Every command takes the root's persistent `--scope`, `--project`, `--user` and
`-C/--root`; `app.scopeRequest` turns them into an `engine.ScopeRequest`.
`Engine.ResolveScope` (`internal/engine/scope.go`) owns the rules: `--user` refuses `--project` and
`-C`; `--scope` (which `--user` and `--project` must agree with), then `--user` or `--project`, then
`-C`, then, on a terminal, `Pick` (the CLI runs the picker); off one, a `.lola/` found walking up,
then `default_scope`, else a refusal naming every way to give a scope, naming the command
(`ScopeRequest.Command`), with `--scope both` only when `ScopeRequest.Both`.

Without `Both`, two scopes from `--scope` or `default_scope` are refused there too. The CLI's
`app.scopes` runs it for the commands besides `install` that write (`uninstall`, `update`, `sync`,
`tidy`, and `lola profile`'s actions and removal); the picker loop (`scopePicker`, which install's
flow shares) asks a one-scope command (`ScopeStep.One`) to tick exactly one, nothing preselected, so
an empty answer asks again.

`sync` takes both scopes and syncs them in order, project roots first, stopping at the first that
fails or is declined; `--json` prints `{"scopes": [...]}`, or carries the synced scopes in the error
document (`syncedScopesError`). `Engine.ResolveReadScope` is the read-only rule (`list`): the flags
when given, `--scope both` included, else the project found walking up and user scope, never asking
and never reading `default_scope`; each `StatusRow` carries its `scope`. `check` and shell
completions use `app.scope`: the flags as one scope, else the found project, never asking.

`lola sync`'s completion asks the engine (`SyncNames`): every module the scope's layers want,
installed or not (a profile's included), and every module installed there. It takes the profiles
lock shared, as a dry run does (none before any lola made the lock file), and no other lock, and
runs no git, so the check that the ledger, `lock.json`, is not tracked by git is left to the command
it completes.

**Install.** `InstallAll` takes, per destination, the scope and its source (`flag`, `config`,
`discovered`, `picked`, from `ResolveScope` or the picker), its targets and its selection edits,
plus global edits, and refuses a destination listed twice and any project rooted at the home
directory (it renders where the user scope does, so two ledgers would claim one set of files; `Sync`
and `Uninstall` still accept one an older lola installed).

Order: the selections lock (`$XDG_STATE_HOME/lola/locks/selections.lock`, always before any scope
lock), then every scope's lock in `lockOrder` (project roots by canonical key, then the user scope,
so two runs never wait on each other in a cycle), then each scope's module resolution; the edits
applied to `selections.yaml` in memory (`applyEdits`: `--only`, then `--skip`, then `--unskip`);
planning through the resolver; every refusal and prompt for every scope (`prepare`: the install
scripts' check, runtime hook consent, `--strict`, `plan.Compute` conflicts, install hook consent,
and trust in the project's scripts, `planScripts`); `config.Save`; then `commit` per scope.

A refusal
or cancel writes nothing; a failure while applying leaves `selections.yaml` written and says the
next `sync` converges. `config.Save` re-reads the file under the lock, replaces only the changed
modules' entries through the yaml.v3 node API (comments and order survive, also in a comments-only
or null document; a section or project key a removal empties is dropped unless it carries a comment,
then it stays as an empty mapping), writes atomically at a symlink's target with mode 0600, and
never overwrites a file that does not load.

**Deletions an install asks about.** `confirmDeletes` (`internal/engine/deletes.go`) asks, before
anything is written, about the files a plan deletes because of the run: every deletion of a run
that changes picks, and the files of a target an explicit `-t` leaves out of a module installed on
it at that scope.

`planInstallModule` adds such a target to the module's `plan.ModuleChange.Targets`
with no intents, so `plan.Compute` deletes its artifacts and drops it from `lock.json`, and records
it in `Result.dropped`; `PickDeletes` labels those removals `target dropped by -t` and the rest
`dropped by your pick change`. The written entry names the narrower targets, so the next sync plans
nothing more. `-y` and the review's Apply answer yes, `--force` needs no answer, `-n` never asks,
and with no way to ask the run is refused (`DeletesRefusedError`) with the run plus `-y`.

**Where-answers in the flow.** The flow's `Context` per destination carries `WritesTo`, the file the
install writes (`installFlow.targetOf` titles each component page with it: `profile go ›
review-kit`). A where-question (`placeEdit`, `Options.AskWhere`) is answered by a picker in the
flow, and the answer is kept per destination and module (`whereKey`, `installFlow.answers`).

`equivalents` prints one `lola install` line per destination and group of modules answered alike,
each with the `--profile` or `--override` that reproduces the answer. The review prints a re-pin of
a listed version as `→ <module>: re-pinned at <short commit>` (`repins`), and for a module an
inherited entry keeps winning (`ModuleScope.Inherited`) says it is up to date, as nothing is
written.

**Choices and the flow.** `InstallChoices` computes everything the pickers show: per destination the
assistants and the manifest's fixed targets; per cached module (sorted by source, then name) its
components with first-line descriptions, and per destination whether it is installed, what is
excluded and which components are installed now.

`Choices.Preview` applies a run's edits to a copy of the selections with the same `applyEdits`, so
the summary shows exactly what the install will do; `ModuleChoice.Diff` turns that into `+ ~ - =`.
`Choices.Versions` and `ModuleChoice.ID` record the cached versions listed; `installFlow` keeps them
as pins (`InstallRequest.Version`) through every "Change …" step, and `pinnedVersion` stops the run
when a pinned version left the cache. Two arguments resolving to different versions of one module
are refused when the choices are computed.

For a module picked from the cache, `listedVersion` lists, per scope, the head of the ref that
scope's entry records (the default branch's for an entry without a ref); a scope with no entry takes
the listed head and records its ref. When no version can be listed (the entry's ref has no cached
head, a ref-less entry whose default branch is not cached, scopes resolving to two versions, a git
head cached before pins with no commit for `pinCommit`) the module is left out of `Choices.Modules`
and `Choices.Warnings` names the command that installs it.

A kept pick that `listedVersion` no longer returns is refused while the choices are computed, before
the summary, as is a module argument whose git version `pinCommit` could not pin. The CLI's
`installFlow` only fills answers: each step asks the `Prompter` (one method per step and one for the
review) unless a flag or the situation answers it, component picks become `--skip`/`--unskip` edits,
and the equivalent commands are built from the same spec.

The flow ends with the review whenever lola chose something the user did not type (`reviews`: a
picker ran, or `ensureAssistants` took the only assistant installed, `auto`), and keeps why each
destination's assistants were chosen (`why`) for the summary. Before the review it runs
`InstallAll` on a copy of the spec with `DryRun` set and the run's output flags (`outputFlags`),
which asks nothing and writes nothing, and prints its findings (`printFindings`) in the summary.

Each pass of the flow numbers the steps it asks, `Steps{At, Of}`: `done` counts the fields asked so
far, and `remaining` forecasts the rest, exactly once what decides a step is known
(`componentPages`, shared with `ensureComponents`, and `ComponentPage.lists`), one field per step
before.

The huh adapter titles each list `Step n of m — <title>` and says `enter: next` under each but a form's
last. It asks the review through `Prompter.Ask` (`huhForms.Review`): `Apply these changes? [y/c/n]`
with no default, under the summary or with it in the viewer by `app.ask`'s fit rule. `y` applies, `n`
and Ctrl+C cancel, `q` quits, Esc goes back a step, and `c` opens the Change menu (`menu`), whose last
entry, `reviewBack`, returns to the review, as Esc there does. With no Change entry the question is
`[y/n]`.

In accessible mode it prints the summary before the full menu (`menu` with no choice, so no default
and a blank line asks again); the review's yes/no questions elsewhere are `askLine` prompts.

`Prompter` implementations: the huh adapter (inline forms on stderr; accessible line prompts under
`LOLA_ACCESSIBLE=1` or `TERM=dumb`, with an `eofReader` that turns end of input into a cancel), the
non-interactive one, and the specs' scripted one. In accessible mode every prompt, hook consent
included, reads through the `eofReader`, so end of input is `ErrAborted`. Specs parse the equivalent
commands back through the real command tree and compare the plans.

The huh adapter draws every form in `lolaTheme` (`internal/cli/theme.go`): huh's Charm theme with
every color, key help included, chosen for WCAG 4.5:1 on a dark or light background, `[x]` and `[ ]`
markers and a bold cursor; without color, `plainTheme` keeps the markers and bold.

The background is resolved once per prompter, before its first form in color, by
`app.darkBackground`: `config.yaml`'s `theme`, else light in accessible mode (no query, no question,
nothing saved), else `lipgloss.BackgroundColor` on stdin and stderr (faked in specs through
`app.queryBackground`; a nil color counts as no answer), else `Prompter.Background` on a terminal,
saved with `config.SaveTheme` (a failed save warns and the answer still holds for the run), else
light.

The theme is wrapped as a `huh.ThemeFunc` that ignores huh's own guess at the background. Pickers
that need a pick validate in the form through `huhForms.required`: its `requiredPick` wrapper
records whether the key huh is handling is Enter, since huh also validates on focus and on every
toggle, so the refusal appears only after Enter with nothing ticked and the next toggle clears it.

The validator accepts once the `eofReader` has seen end of input, because huh's accessible
MultiSelect asks again while Validate refuses and reads end of input as `0`. A module picker of
several lists asks again after the form instead, because huh validates a field on tab and shift+tab
too. So the flow never receives an empty Scope, Modules, Assistants or Required TidyKeep answer.

lola's keys, `q` quits and Esc goes back a screen (`internal/cli/keys.go`), live in the adapter and
the flows, never in the engine. `huhForms.form` wraps every field of a full-screen form that takes
keys (a note does not) in `keyed`, which sees each key before huh: `q` quits unless the field takes
it as text (a `huh.Input`, or a list whose `GetFiltering` is true); Esc goes to the form's field
before by handing huh Shift+Tab, or, on the form's first field, back a screen, unless one of the
field's enabled bindings is `esc` (huh's set and clear filter).

Both end the form through its Quit binding (Ctrl+C, the only key huh's default keymap gives it) and
record which in the form's `formExit`, so `runIn` answers a `quitError` or `ErrBack` instead of
`ErrAborted`.

Accessible mode wraps nothing. Each step struct carries a `Nav`: `Back`, whether there is a screen
to go back to (without it, Esc puts `first step — q quits` in the key help), and `Answered`, the
steps answered so far; with `Answered` plus the fields before it at two or more, `q` first asks
`quitQuestion` in the key help's place (`keyed.Error` hides a field's error meanwhile).

`KeyBinds` puts `esc back` and `q quit` before huh's own help, which alone fills 80 columns.
`quitError` is `ErrAborted` to `errors.Is`; `Execute` prints it as `style.bad("cancelled — nothing
was changed")`, in the forms' color, as it prints every other `ErrAborted` (without color); a command
that turns `ErrAborted` into its own `abortedError` keeps its wording, printed through `style.bad`
too (`✗ cancelled — the project was not changed`).

A Prompter method whose `Nav.Back` is set answers `ErrBack` for Esc, and its caller asks the screen
before again. The install flow's `answer` runs one pass's `ensure*` steps and names the stage that
failed; `run` hands `ErrBack` from a stage, or from the review, to `back`, which forgets the answer
of the screen `before` names, kept as its preselection, and the loop's next pass asks it and carries
on.

`before` is, on a component page or the review, the last page held (`pageLog`, the pages answered in
order, each a module and its destinations, `holds` checking that its picks are still kept) asked
before it, and otherwise the last picker `shown` of those for the scope, the modules and the
assistants; forgetting a picker reuses the review's Change machinery (`review`), forgetting a page
moves its picks to `prevPicks`. `nav` derives each step's `Nav` from `before` and `held`, the
answers held from screens the user answered.

`askPage` asks a page and, when nothing is picked of a preselection, `Install <module> with no
components?` (`askYes`), whose Esc asks the page again with nothing ticked. Inside the adapter, Esc from Add a source… answers the picker's
`AddSource` with no source (back to the picker), from Choose… asks the trust question again, and
from the review's Change menu shows the review again; `scopePicker.pick` asks the scope picker again
from Other directory….

`tidyChoose` asks the move picker again, preselected, from the keep picker; `askMappings` walks its
root questions by index and, through `askTargets`, the assistant questions, recomputed from the
roots answered, going back from the first of them to the last root. `Prompter.Ask` takes a `Nav`
too (in its `Question`): `tidyChoose` and `askMappings` return the steps they asked, which tidy's
closing question and the "Migrate?" question pass as `Answered` (no `Back`: the engine asks them after the pickers),
so `q` there asks first after two answers, and Esc's note reads `no step to go back to — q quits`
rather than `first step`; every other caller passes `Nav{}`.
