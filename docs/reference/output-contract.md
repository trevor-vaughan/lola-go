# Output contract

The output formats lola's commands share. It is split out of [usage](../usage.md).

- `--json`: one JSON document on stdout, snake_case keys, arrays always present (`[]` when empty).
- `--plain`: tab-separated lines, no color, every row starting at column 1 (its kind first), so
  `cut -f1` reads it.
- Human output merges four or more warnings that differ only in the artifact (same module,
  target, kind, code, text and hint) into one line, `⚠ ecc: opencode agent: model dropped:
  opencode needs a string matching ^[^/]+/.+$ (71 agents: a11y-architect, agent-evaluator,
  architect, …; --json lists each)`, with the artifact's name shown as `…` in the text and
  `<artifact>` in the hint. Run with `--json` to see every warning; `--plain`, `--json` and
  `--strict` are unchanged.
- `install`, `sync`, `update` and the `lola profile` actions that apply print safety findings
  and notices in full ([Findings](#findings)) and the detail ones as one line per module:
  `→ ecc: 61 notices (claude-code 20, gemini-cli 21, opencode 20) — lola check --project ecc
  -t claude-code,gemini-cli,opencode`.
  - Its `lola check` keeps the run's scope and output flags and lists each with the hints for the
    module's author, and what the picks leave out that the module mentions (counted as `module`).
  - The check names the module by its cache name, or, in a dry run of a version your cache lacks,
    by its source (the review before Apply counts as one).
  - Detail no `lola check` can name is printed in full: a git source with `--ref` or `--path`, a
    version that is not your cache's newest for its source (a pin behind it, or another ref), or a
    cache name that a folder or archive of that name in the current directory would stand in for.
  - Under `--scope both` each scope has its own line and check.
  - `-v`/`--verbose` prints them all, grouped as above, without the author's hints, in the review
    before Apply too; `--plain` prints a row each and `--json` lists each with its `class`.
- A notice in JSON is `level`, `text` and `class`, and `hints` when it has commands that act on it
  (a list, absent otherwise, such as the one for an assistant with versions that is not installed);
  human output prints each hint under it as a `hint:` line. When stdout and stderr are both a
  terminal, a hint wider than it wraps at spaces, but a `lola …` command in it is never broken (one
  wider than the terminal gets a line of its own); a pipe or a file gets each hint on one line. A
  `--plain` notice row is `notice<TAB>level<TAB>text`; in a `sync` or `install` of `--scope both`
  it ends with the scope it is for (`project` or `user`).
- Scope commands (`install`, `uninstall`, `sync`, `update`, `list`, `info`, `check` when it reads
  the scope's entries, `tidy`, `repair`, and `lola profile` listing, showing, `--on`, `--off` and
  `--switch`) print one context line per scope:
  - Human output: on stderr, once the scope is resolved: `project /home/me/src/acme: profile go
    over project.yml`, `user scope: profile go over default` (`user scope: default` with none on);
    a bundle on is named with its members: `user scope: bundle work (go-review, docs) over
    default`. After `--on`, `--off` or `--switch` changes the profile on, human output ends with
    `now: <context line>`.
  - `--json`: their document starts with `context`, and their refusal carries it as one key among
    the error's fields, which are sorted by key. It is one object per scope with `scope`, `root`,
    `base` (`name`, `kind`, `path`), `profile` (`name`, `kind`, `path`, `bundle`, `members`, or
    `null` when only the base is loaded), `writes_to` (the file `install` and `uninstall` edit, as
    `name`, `kind`, `path`, else `null`) and, when lola could not tell the profile on, `error`. In
    each object `profile` and `writes_to` may be `null`; arrays never are.
  - `--plain`: each scope's context is a row on stderr, beside where the human line goes, so stdout
    keeps the command's rows: `context`, the scope, its root (empty at user scope), its base, the
    profile on by qualified name, `-` when none is on or `unknown` when lola could not read the
    scope's state, and a bundle's members, comma-separated
    (`context<TAB>user<TAB><TAB>default<TAB>user:work<TAB>user:go,user:docs`), one row per scope.
  - A remote profile on: while the profile on is a remote profile that `pins.yaml` records and
    this machine lacks, the object's `restore` is the `lola sync` command that fetches it (absent
    otherwise) and the human line reads `profile remote:go (not on this machine yet → lola sync
    --user) over default`.
  - A bundle with a remote member this machine lacks reads `bundle work (go-review, remote:x)
    (remote:x not on this machine yet → lola sync --user) over default`.
  - While a file or link is in its place, `error` names it instead
    (`<path> is in the way of it; move it aside`).
- Errors: `error: <message>` on stderr with indented `hint:` lines; under `--json`, one line
  `{"error": "...", "hints": [...]}`, which some refusals extend:
  - A sync refused for want of an answer adds `trust` (its `status`, and `changes: []` when only
    the removals are missing) and `removals` (`[]` when only trust is missing), both always
    present, so CI gets the changes without a second run (`lola sync -n --json` adds their
    content).
  - A [template](../guides/templating.md#templates) refusal adds `template`: `module`, `target`,
    `scope`, `kind`, `artifact`, `file`, `line` (0 when none applies), `code`, `reason`,
    `included_from` (the include site, `<file>:<line>`, of an error in a
    [partial](../guides/templating.md#partials), whose `file` it is; else `""`) and `rendered` (true
    when the rendered output, not the template, was refused). For `template-run-budget` the
    target, kind, artifact and file are `""`.
  - A confirmation refused off a terminal (`uninstall`, `cache rm`, `cache clean`, `migrate`,
    `repair` and a bare `init` without `-y`) prints only that line, which adds the plan it would
    have asked about; its hint is the run with `-y`:
    - for `uninstall` and `migrate`, the keys their `-n --json` prints (`modules`, `steps`,
      `findings`, `notices`, `edits`, `scripts`, `picks_cleared` and `dry_run` for `uninstall`);
    - for `cache rm`, `would_remove`, the names;
    - for `init`, the keys its `-y --json` prints (`name`, `path`, `format`, `files`);
    - for `cache clean`, the keys its `-n --json` prints (`removed`, `kept`, `bytes_freed`,
      `dry_run`, `notices`);
    - for `repair`, the keys its `-n --json` prints (`dry_run`, `file`, `forgotten`,
      `forgotten_modules`, `notices`).
- Human output says a hook or install script `would run` in a dry run; `--json` and `--plain`
  keep the value `would-run`.
- The results of `install`, `uninstall`, `update` and `sync` (and so of `profile --on`, `--off`
  and `--switch`) carry `scripts`: each install script the run ran, would
  run, declined or saw fail, per module and assistant, in run order (`[]` when none; [Install
  scripts](../guides/choosing-what-to-install.md#install-scripts)); its `path` is shown as configured, with control characters replaced.
- `sync`, `profile`, `install`, `update` and `migrate` under `--json` carry `trusted`: each item
  the run's `--trust` covered, `{kind, item, change}` with `change` `new` or `changed`, `[]` when
  none; under `-n`, what it would cover ([Trust kinds](../guides/choosing-what-to-install.md#trust-kinds)).
- Exit status:
  - `0` on success or when you decline a prompt (Ctrl+C included, in accessible mode too).
  - `1` on any error, which includes a module whose fetch `lola update` could not make.
  - Ctrl+C (SIGINT) or SIGTERM elsewhere stops the fetch, download or hook the command is waiting
    on, or its writes before they start, removes its temporary files, and lola then dies of the
    signal, so your shell reports `130` or `143` and a script running lola stops too.
  - It prints nothing then, but for a `lola profile --update` that already wrote a profile: that
    error says what is updated but not applied, and the `lola sync` that applies it (under
    `--json`, the error document).
  - A second Ctrl+C ends it at once.
- Color: `LOLA_COLOR=1` or `0` forces it; otherwise `NO_COLOR` disables it; otherwise it follows
  the terminal.
- Picker colors are chosen to read at a contrast of at least 4.5:1 on your terminal's
  background, which lola takes from the first of:
  - `theme: dark` or `theme: light` in `config.yaml`;
  - in accessible mode, light (lola neither queries the terminal nor asks there, and saves
    nothing);
  - what the terminal reports (a terminal that answers without a color counts as not
    answering);
  - on a terminal, your answer when lola asks once, `Is your terminal background dark or
    light?`; it saves the answer (`→ saved theme: dark in <config.yaml>`), and if it cannot save
    it, it warns, with the line to write yourself, and uses your answer for that run;
  - else light.

  Without color, pickers keep `[x]`, `[ ]` and the bold cursor `>`.
- The viewer:
  - When stdout and stderr are terminals, human output and help taller than the terminal open
    in lola's viewer, the same on every OS. Output that fits is printed as it is.
  - `↑`/`↓` or `j`/`k` scroll a line, `PgUp`/`PgDn` or `b`/space a page, `g`/`G` or
    `Home`/`End` go to the top or the end, and `q`, Esc or Ctrl+C quit; the footer shows where
    you are (`lines 41–80 of 312 · 25%`).
  - `/` searches:
    - Type a term and press Enter to jump to the first line at or below the top that holds it,
      with that line in reverse video and the footer saying which match it is (`/trust 2 of 7`).
    - A term in lowercase matches any case; one with a capital matches exactly.
    - `n`/`N` go to the next and previous match, wrapping at the ends; in a confirmation's plan,
      where `n` cancels, they are Ctrl+N and Ctrl+P.
    - Esc stops typing, and Enter on an empty term clears the search.
    - A term split across a wrapped line is not found.
  - On quit the full text is printed, so it stays in your scrollback.
  - The plan a confirmation question follows (`uninstall`, `cache rm`, `cache clean`,
    `migrate`, a bare `init`) opens the same way, and the question comes after you quit it.
  - In order, `--json` or `--plain`, a stream that is not a terminal, accessible mode
    (`LOLA_ACCESSIBLE=1` or `TERM=dumb`), `--no-pager`, `NO_PAGER` set to a non-empty value, and
    `pager: false` in your `config.yaml` print without it.
  - A `$LOLA_PAGER` you set runs that pager instead (`cat` prints). `$PAGER` is not read.
- Without a terminal, `uninstall`, `cache rm`, `cache clean` (when it has something to remove),
  `migrate` and a bare `init` need `-y`; `tidy`
  needs names or `--all`, and `-y`. `sync` needs `-y` to
  remove anything and `--trust` to accept changes to `project.yml` (`--trust <module>…` for some),
  including the pin it writes back for an unpinned entry you trusted before; `-y` never trusts.
- `install --json` adds `scopes` (each with `scope`, `root` and `scope_source`), and per
  module `scope`, `skipped` (component names, `[]` when none) and `saved_to` (the
  `selections.yaml` or the profile it saved the picks to, or `""`), `rewritten` (`true` on a
  module that `--force` did not install but rewrote for the version the install writes for, under
  `-n` too; absent otherwise), `equivalent_commands`
  (the review's; under `-n` with no review, the run that applies the plan, as `to apply:` prints
  it; `[]` otherwise), and `target_versions` (`[]` when it records none): one object
  per version the install records in `target_versions` (would record, under `-n`), `{scope,
  target, version, previous, source, file, era}`.
  - `previous` is the version the scope declared before, `""` for none; `era` is the `since` of the
    era the files are written for; `file` is the `project.yml` or `config.yaml` it goes in.
  - `source` is `installed` (the installed assistant's, where lola assumes it), `chosen` (your
    answer to the version question) or `flag` (`-t <id>@<version>`).
- `install`, `uninstall`, `sync`, `update`, `profile --on`, `--off`, `--switch` and `--move` carry `edits`
  (`[]` when the run wrote no entry): one object per entry lola wrote into a file, `{module,
  action, profile, kind, path, scope}`.
  - `action` is `added`, `changed` or `removed`.
  - `profile` is the file's qualified name (`default`, `user:go`, `project:team`, or `project.yml`
    for a project's base).
  - `kind` is `base`, `user` or `project`; `path` is the file; `scope` is `project` or `user`.
  - `module`, `profile` and `path` have control and format characters replaced.
  - `sync` and `update` list the re-pins they write.
  - A move writes `removed` and `added` rows, never `moved`.
  - `--plain` prints each as `edit<TAB>action<TAB>module<TAB>profile<TAB>path`, before the steps.
  - Under `-n`, `edits` lists what the run would write.
- `list --json` and `check --json` carry `skipped` per module.
  - `list --json` rows carry `scope` (`project` or `user`) and `from` (the
    [layer](../guides/profiles.md#profiles) the module comes from: `project.yml`, `profile:default`,
    `profile:user:<name>`); `list --plain` rows end with the two.
  - `list --json` has `notices` (`[]` when none), as `--plain` has `notice` rows.
- `sync --json`:
  - Top level: `scope`, `root`, `dry_run`, `in_sync`, `trust`, `pins`, `removals`, `rewrites`
    (`[]` when none: one `{module, target, from, to}` per module whose files the sync rewrites for
    another era of a target, `from` and `to` the eras' `since`), `equivalent_commands` (in a dry run that is not in sync, the command that applies the plan,
    with the `--trust=` kinds it needs off a terminal; when the plan removes files, off a terminal
    that command is refused until you add `-y`, which it carries only when you gave it; otherwise
    `[]`), and the plan's `modules`, `steps`,
    `findings` and `notices`.
  - `trust` has `status` (`trusted`, `new`, `changed`, or `not_applicable` at user scope),
    `recorded` (this run wrote the trust record), `targets` (the top-level targets' `field`,
    `from` and `to`; omitted when trusted), `target_versions` (the same for `target_versions`,
    `from` and `to` each an object of target id to version, `null` when there was none or is none
    left; omitted when trusted), `profile_targets` (the same for a project profile's
    top-level `targets`, each with its `profile`; omitted when trusted), `changes` and
    `replacements`.
  - Each of `trust.changes` is per module:
    - `module`; `profile` (the project profile, `project:<name>`, the change is in; absent for
      `project.yml`); `change` (`added`, `changed`, `removed`); `host_changed`;
      `content_fixed`; `note` (for an entry that does not pin its content).
    - `fields` (`field`, `from`, `to`: strings, a list for `targets`, a boolean for `rules`,
      `null` for an absent value).
    - `reason`: why an entry the manifest has as trusted needs trust (the pin sync would write,
      or a folder's changed content).
    - `files` (`path`, `change`; every file, `lola.yml` only when it changed or went) and
      `files_note`.
    - `mcp_servers`: `name`, `transport`, `change` (`added`, `changed`, `unchanged`, `removed`,
      against the version installed), `runs`, `env` as `NAME=value` strings and `headers` as
      `Name: value` strings (each sorted and `[]` when none), `cwd` (`""` when unset), and, only
      for a `changed` one, `previous`: its installed form with the same keys but `change`.
    - `hooks`: `kind` (`install`, `runtime` or `mcp_server`), `target`, `event`, `detail` and
      `change`: `added`, `changed` (an install hook whose script file changed), `unchanged` or
      `removed`.
  - `trust.replacements` (`[]` when none): per project profile an activation puts in place of
    your active namesake, `profile`, `replaces` (the profile it replaces) and `modules` (those it
    changes); `status` is `changed` when only a replacement needs trust.
  - `pins`: `module`, `profile` (the project profile the pin is written into; absent for the
    manifest's), `ref`, `commit`, `digest`, `previous_digest`, `reason` (`unpinned` or
    `update`), `written` (false in a dry run).
  - `removals`: `module`, `targets`, `whole_module`, `paths` (each `target` and `path`), `scope`
    (`project` or `user`), and `why` when the run itself is not the reason. An install's removals
    at two scopes (`--scope both`) add `where` (`project <root>` or `user scope (<home>)`), which
    the human list prints after each block's reason.
  - Commits and digests are full.
  - With two scopes (`--scope both`), `sync --json` prints `{"scopes": [...]}`, one such
    document per scope in order; when a scope fails, the error document carries the scopes
    synced before it in `scopes` (`[]` when the first failed).
  - A refusal for lack of a terminal prints the changes on stderr first, except under `--json`.
  - `--plain` adds `pin` (module, digest, reason) and `remove` (module, target, path, scope) rows
    after the plan's; every `remove` row has the scope column (`project` or `user`), one scope or
    two, and a refusal that lists removals (`install`, `sync`, `update`, `lola profile`) prints the
    same rows on stderr.
  - In a dry run, `--plain` prints `trust` rows (module, change, and the project profile the
    change is in, empty for `project.yml`; the top-level targets' row has an empty module and
    the change `targets`, and a `target_versions` change has a row per target, with an empty
    module and profile, then the target, its version before and after, each empty when none:
    `trust<TAB><TAB>target_versions<TAB><TAB>opencode<TAB>2.0<TAB>1.0`) before the plan's, after a `replace` row (profile, the profile it
    replaces) for an activation that puts a project profile in place of yours.
- Text a module or a manifest chose shows control and invisible characters as `?`, in JSON as on
  a terminal:
  - It covers names, paths, hook scripts such as `modules[].hooks[].script` and the scripts of
    install hooks, `mcp_servers[].runs`, module warnings, the trust diff, and the names of the
    module's files in `lola info`, human or `--json`.
  - `runs` and a hook's `script` are for reading, not an exact command line to run.
  - A step's `path`, `member` and `value` are the exception: they are what lola wrote, raw but
    for JSON's escaping.
  - So are `lola tidy`'s `path` and `trash`, the paths it found, moved or wrote, which may hold
    Unicode format characters a terminal would not show.
- `migrate --json`:
  - `dry_run`, `nothing_to_migrate`, `python_home`, and `trash` (`""` when nothing was put
    there).
  - `home`: `action` `move`, `keep` or `none`, with `reason`, `trash` and `hints`.
  - `unmappable` and `not_migrated`: each entry with its `hints`.
  - `markets`: the Python lola's marketplaces as lola registers them, each `name`, `url`,
    `market`, `source`, `path`, `status`, `reason` and `hints`.
  - `warnings`.
  - `summary`: the Python lola's records by outcome: `migrated` (on a dry run, those that
    would), `already`, `left` and `failed`.
  - `remap`: the run that maps what this machine lacks, `""` when no flag would.
  - `destinations`, each with its `status`: `planned`, `migrated`, `unchanged`, `nothing` when
    every record there is left as it is, or `failed`, with `error` and `hints`.
  - Per destination also `installs`, `replaced` and `moved` (each `path`, `key`, `kind`,
    `trash`), `kept` (`path`, `reason`), its own `unmappable` and `not_migrated`, and the
    install's `steps`, `modules`, `findings` and `notices`.
  - A step that replaces a recorded file has `"replaced": true`; so does an `adopt` step on a
    recorded file that is already identical to what lola writes, which lola keeps and does not
    rewrite. The human plan line says which: `[replaces a file lola did not write]` for the
    first, `[identical to a file lola did not write; kept, not rewritten]` for `adopt`.
