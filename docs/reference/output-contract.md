# Output contract

Command reference, for lola users; back to [Using lola](../usage.md) and the [README](../../README.md).

The output formats lola's commands share. It is split out of [usage](../usage.md).

- `--json`: one JSON document on stdout, snake_case keys, arrays always present (`[]` when empty).
- `--plain`: tab-separated lines, no color, every row starting at column 1 (its kind first), so
  `cut -f1` reads it.
- Human output merges two or more warnings that differ only in the artifact (same module, target,
  kind, code, text and hint) into one line, `⚠ ecc: opencode agent: model dropped: opencode needs a
  string matching ^[^/]+/.+$ (71 agents)`.
  - The artifact's name is shown as `…` in the text and `<artifact>` in the hint, then one
    indented line per artifact (`a11y-architect`, `agent-evaluator`, …).
  - In a run's report those lines are the group's detail: shown in full under a section of new
    findings, folded under "Unchanged since last run" until you open the fold or pass `-v`.
  - `--plain`, `--json` and `--strict` are unchanged, and `--json` lists every finding.
- A run's report (`install`, `uninstall`, `sync`, `update` and the `lola profile` actions that
  apply) has these sections, each under a `Title:` line (bold on a terminal) and omitted when
  empty: `Runtime hooks — new`, `Warnings — new`, `Skipped — new`, `Hooks and scripts`, `MCP
  servers`, `Notices`, `Unchanged since last run`, `Files`, `Removed` (`To remove` in a plan),
  then the outcome. A dry run of `install`, `sync` or `update` that would change nothing (no
  step, edit, pin or removal, every runtime hook granted or declined as before, no warning or
  skip new to the user, no warning among its notices, and under `-v` no finding, notice or
  runtime hook for it to print) prints only its outcome (`already in sync`, or an install's
  `<module> already up to date (<targets>)`), without the plan header. Any other install dry run
  ends, as the real run does, with `→ <module> already up to date (<targets>)` for each module it
  would leave as it is, under `--force` too unless its install hooks would run again.
  - A safety warning or skip, or a runtime hook, is new when lola has not shown it for that module
    before.
    - In `lock.json` a module's entry records `safety`, the keys of its state findings
      (pre-approved tools, permission modes, load warnings, native executables, skipped artifacts
      and those `--force` rendered anyway, a fact apart from the skip, and runtime hooks, declined
      or installed), on every run that is not a dry run, whatever the output mode.
    - A changed tool list, or a changed hook, is new again, while an unchanged one stays seen when
      its module changes.
    - Every other warning or skip is new on every run; an `info` finding is neither new nor seen.
    - A runtime hook consented to in an earlier run is seen.
    - One granted in this run is new unless an earlier run installed it as it is (a new module
      version asks again).
    - One declined is new unless an earlier run declined it as it is.
  - A target's declined settings hooks print in their block under `Runtime hooks — new` (or in the
    fold once seen), ending with the hint that consents to them, in place of their
    `runtime-hooks-declined` warning; `--plain` and `--json` keep that finding.
  - What is seen is one fold, `▸ N warnings, M skips, K runtime hooks seen before`, with each
    module's count and the `lola check` or `lola info` that lists them. Runtime hooks declined
    before, which the run did not ask about again, count apart (`; 1 runtime hook declined
    earlier`), their block's status `declined earlier`.
  - Under `Hooks and scripts`, each install hook and install script names its sha256:
    `kit pre-install hook hooks/pre.sh (sha256 …, claude-code): would run`.
    - A hook's or project script's body follows once, in full, `    | ` before each line (a tab
      expanded to 8-column stops, control characters replaced), until lola has shown it, then as a
      fold (`▸ hooks/pre.sh: 12 lines`); an empty one is shown by its line.
    - One withheld (too large or not text) prints `(not shown: too large; inspect <file>; only
      --unseen consents to it)`.
    - A dry run with a hook or script lola has not shown prints it, never only its outcome.
    - Each project script (pre- or post-install) a dry run with nothing to install checks is listed
      alone, `pre-install script ci/check.sh (project at …, sha256 …): trusted; nothing to wrap
      now` (`status` `later`), or, for one without the trust a run would need, `not trusted yet; to
      trust it: lola sync --project --trust=scripts` (with `--unseen` added for one lola cannot
      show).
    - Every script in `--json` carries `trusted` (a boolean: `false` for a project script a dry run
      lists without the trust a run needs for it) and `trust_run` (for a script that is not
      trusted, `would-run` or `later`, the command that trusts it, with your output flags; `""`
      otherwise).
    - A `--plain` `script` row ends with `sha256`, `trusted` (`true` or `false`) and `trust_run`
      (`-` when empty).
    - Not to be confused with the run's top-level `trusted`, the kinds `--trust` covered.
  - After the install review's Apply (or its summary printed under `--force`), the new
    warnings, skips and runtime hooks the review showed, the same text and hint, are one fold
    first, with no header: `▸ N warnings, M skips, K runtime hooks shown in the review above`.
  - The files are a fold per module (`▸ kit: 12 changes (claude-code 6, opencode 6)`).
  - A closed fold prints its summary, marked `▸`; printed output, and a pipe, keeps every fold
    closed and ends with `→ ▸ lines are folded; -v prints them in full`. `-v` prints each fold's
    detail, and `--plain` and `--json` are always complete. The text of a question printed above
    its prompt, or handed to `$LOLA_PAGER`, has every fold open and no footer, since nothing
    there can open one; the viewer keeps them closed for you to open.
- `install`, `sync`, `update` and the `lola profile` actions that apply print safety findings
  and notices in full ([Findings](../usage.md#findings)) and the detail ones (defined there) as one line per module:
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
- In a run, a finding in `--json` carries `new` (`true` or `false`) when it is a safety warning
  or skip; an `info` finding (such as `probe/unknown-version`), and `lola check` and `lola info`
  findings, do not carry it.
  - A runtime hook's `status` is `granted`, `consented` or `declined`, and in a run it carries
    `new` too.
  - In `--plain`, a run's `finding` row for a safety warning or skip ends with `new` or `seen`, and
    its `runtime-hook` row has `new` or `seen`; `lola check` and `lola info` rows have no such
    column.
  - A `runtime-hook` row is
    `runtime-hook<TAB>module<TAB>target<TAB>event<TAB>type<TAB>status<TAB>component`, the component
    (`<kind> <name>`) whose frontmatter has the hook, `-` for a hook in a settings file, then `new`
    or `seen` in a run.
  - Every `runtime-hook` row ends with the hook in full, its lines joined by `; ` (`Stop: command;
    runs: ./stop.sh`).
  - A run's `hook` and `script` rows end with the script's sha256, each followed by its body, a row
    per line: `hook-line<TAB>module<TAB>event<TAB>script<TAB>line`,
    `script-line<TAB>module<TAB>event<TAB>path<TAB>line`, or, for a body withheld,
    `withheld<TAB>reason<TAB>file`.
- A notice in JSON is `level`, `text` and `class`, and `hints` when it has commands that act on it
  (a list, absent otherwise, such as the one for an assistant with versions that is not installed).
  - Human output prints each hint under it as a `hint:` line.
  - When stdout and stderr are both a terminal, a hint wider than it wraps at spaces, but a
    `lola …` command in it is never broken (one wider than the terminal gets a line of its own); a
    pipe or a file gets each hint on one line.
  - A `--plain` notice row is `notice<TAB>level<TAB>text`; in a `sync` or `install` of
    `--scope both` it ends with the scope it is for (`project` or `user`).
- Scope commands (`install`, `uninstall`, `sync`, `update`, `list`, `info`, `check` when it reads
  the scope's entries, `tidy`, `repair`, and `lola profile` listing, showing, `--on`, `--off` and
  `--switch`) print one context line per scope:
  - Human output: on stderr, once the scope is resolved: `project /home/me/src/acme: profile go
    over project.yml`, `user scope: profile go over default` (`user scope: default` with none on);
    a bundle on is named with its members: `user scope: bundle work (go-review, docs) over
    default`. After `--on`, `--off` or `--switch` changes the profile on, human output ends with
    `now: <context line>`.
  - `--json`: their document starts with `context`, and their refusal carries it as one key among
    the error's fields, which are sorted by key.
    - It is one object per scope with `scope`, `root`, `base` (`name`, `kind`, `path`), `profile`
      (`name`, `kind`, `path`, `bundle`, `members`, or `null` when only the base is loaded),
      `writes_to` (the file `install` and `uninstall` edit, as `name`, `kind`, `path`, else `null`)
      and, when lola could not tell the profile on, `error`.
    - In each object `profile` and `writes_to` are `null` only where lola could read the scope and
      there is none; when it could not (`error` is set) they are objects whose strings are all `""`
      (and `members` `[]`), where an empty value means unknown.
    - Arrays never are `null`.
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
- Progress: on a terminal, `lola update`, `lola sync` (and the `lola profile` changes that sync),
  `lola install` and `lola cache update` show what a long run is doing (`… fetching kit (3/12)
  from <source>`) on one line of stderr that they redraw and clear before anything else prints.
  - In accessible mode each step is a line of its own.
  - A fetch over ssh prints its step as a line that stays, since ssh may ask on the terminal, and
    nothing is drawn while ssh, a script or a hook runs.
  - Scripts, `--json` and `--plain` get none.
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
      `findings`, `notices`, `edits`, `scripts`, `trash`, `trash_credentials`, `picks_cleared` and
      `dry_run` for `uninstall`);
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
  scripts](../guides/choosing-what-to-install.md#install-scripts)).
  - Its `path` is shown as configured, with control characters replaced, and `sha256` is the
    script's digest when the run planned it.
  - A project script carries `body`, the script's text raw, so its sha256 can be checked (JSON
    escapes control characters), and `new`, whether lola had not shown it.
  - A run's `hooks` carry `body` and `new` the same way.
  - A script larger than 64 KiB, or not UTF-8 text, or holding a NUL, has `body` `""` and
    `withheld` `too_large` or `not_text`: output never shows it, so only `--unseen` consents to
    it.
- The results of `install`, `uninstall`, `update` and `sync` (and so of `profile --on`, `--off`
  and `--switch`) carry `trash`: each trash run, by path, holding a copy of a file `--force`
  replaced or deleted, one per scope written (`[]` when none, and always under `-n`), and
  `trash_credentials`: each copy in those runs that may hold credentials, the whole file of a JSON
  member `--force` wrote over or removed (`[]` when none). Human output prints `→ N files
  --force replaced or deleted are in the trash: <run>` (`1 file … is`) per run and warns `the trash
  run holds a copy of <file>, which may contain credentials` for each credential copy; `--plain`
  prints a `trash` row per run (`trash`, run).
- A runtime hook in a run's JSON carries `declined_earlier: true` when it was declined, as it is,
  by an earlier run, so this one did not ask about it; it is absent otherwise.
- Output that prints an install hook, project script or runtime hook in full (human output of what
  is not folded, a run's `--plain` rows and `--json` document, `lola migrate`'s preview, a
  question, and the runtime hooks `lola info` renders) adds it to
  `$XDG_STATE_HOME/lola/shown.json`, which `--trust`'s consent kinds grant from ([What lola has
  shown you](../guides/install-trust-kinds.md#what-lola-has-shown-you)).
  - So does a confirmation refused off a terminal (`… needs confirmation, and there is no terminal
    to ask on`), whose plan prints the bodies in human output and carries them under `--json`.
  - A consent refusal (a pre-install hook or script without consent), which names a script but
    prints no body, a folded line and `lola info`'s first lines of a hook do not.
  - When the record cannot be written the run still succeeds, with a warning on stderr (none under
    `--json`).
- `sync`, `profile`, `install`, `update` and `migrate` under `--json` carry `trusted`: each item
  the run's `--trust` covered, `{kind, item, change}` with `change` `new` or `changed`, `[]` when
  none; under `-n`, what it would cover ([Trust kinds](../guides/choosing-what-to-install.md#trust-kinds)).
- Exit status:

  | Code | Meaning |
  |---|---|
  | `0` | success, or nothing to do |
  | `1` | an error, which includes a module whose fetch `lola update` could not make |
  | `2` | unused, kept for usage errors |
  | `3` | lola changed nothing because the run needs something only you can give: `--trust` kinds, consent, `-y`, or `--force` over a conflict, or you gave none, such as end of input; rerunning with the hint's flags proceeds |
  | `4` | you declined or cancelled (a `n` answer, `q` in a picker or form, Ctrl+C in a form or question) |
  | `128+n` | killed by signal `n`, unchanged |

  - A conflict `--force` cannot override (a file another module owns: uninstall that module first)
    is an error: `1`. A No you typed at a hook or script consent question prints the refusal and
    exits `4`; with no terminal to ask on, it is `3`.
  - With several scopes, a refusal in a later scope can follow an earlier scope that was applied; the
    output says which. So can the refusal of `lola profile --update`'s sync, which follows the profiles
    it already wrote.
  - The refusal's text, hints and `--json` document are the same as for any error; only the code
    differs, so a script tells "answer needed" (`3`) from a failure (`1`).
  - `q` in the `lola help` viewer only closes it: exit `0`.
  - Ctrl+C (SIGINT) or SIGTERM elsewhere stops the fetch, download or hook the command is waiting
    on, or its writes before they start, removes its temporary files, and lola then dies of the
    signal, so your shell reports `130` or `143` and a script running lola stops too.
  - It prints nothing then, but for a `lola profile --update` that already wrote a profile: that
    error says what is updated but not applied, and the `lola sync` that applies it (under
    `--json`, the error document).
  - A second Ctrl+C ends it at once.
- Color: `LOLA_COLOR=1` or `0` forces it; otherwise `NO_COLOR` disables it; otherwise it follows
  the terminal.
- Bold (section headers, rules): on a terminal, where `NO_COLOR` and `LOLA_COLOR=0` keep it, as
  emphasis rather than color; never into a pipe or a file, under `TERM=dumb` or under `--plain`;
  `LOLA_COLOR=1` forces it.
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
    `Home`/`End` go to the top or the end, and `q`, Esc or Ctrl+C quit; a bold rule under the
    text shows where you are (`━━ lines 41–80 of 312 · ↓ 232 more`), and the line under it the keys.
  - `/` searches:
    - Type a term and press Enter to jump to the first line at or below the top that holds it,
      with that line in reverse video and the key line saying which match it is (`/trust 2 of 7`).
    - A term in lowercase matches any case; one with a capital matches exactly.
    - `n`/`N` go to the next and previous match, wrapping at the ends; with a question in the
      viewer, where `n` is an answer, they are Ctrl+N and Ctrl+P.
    - Esc stops typing, and Enter on an empty term clears the search.
    - A term split across a wrapped line is not found.
    - A closed fold's lines are not searched; when only they hold the term, the key line says so:
      `/curl no match (2 in closed folds — tab, space to open)`.
  - A report with folds shows `tab  next fold · space  open` in the key line. `tab`/`shift+tab`
    move a highlight between the folds' summaries and `space` opens or closes the highlighted one
    (with none, `space` pages), scrolling a fold it opens into view: as much of its detail as
    fits, its summary at the top when it does not all fit. Esc clears a highlight before it
    quits; in a question that has a step before it, Esc goes back.
  - On quit the full text is printed, with the folds you opened open, so it stays in your
    scrollback.
  - Before a question, a picker or a form, lola draws a blank line and a bold rule of `━` across
    the terminal (`---` in accessible mode), so the eye finds where to answer. A run of pickers
    and forms, such as `lola install`'s steps, which leave nothing behind, shares one rule; each
    question gets its own.
  - A yes/no question (`uninstall`, `cache rm`, `cache clean`, `migrate`, a bare `init`, the
    removals `sync` asks about, the profiles `profile --add` and `--update` ask about, hook consent,
    `Proceed?`) and the install review sit under the text they ask about: printed, the prompt
    `Uninstall mymod? [y/N]` on the next line, when both fit the terminal; otherwise in the viewer,
    the prompt between the rule and the keys.
    - Type the answer's letter and press Enter; Enter alone takes the capital letter's answer
      (`[y/N]`: no), and the review (`[y/c/n]`) has none.
    - Esc goes back where there is a step before, `q` quits, Ctrl+C cancels (`✗ cancelled — nothing
      was changed`, exit 4).
    - With `$LOLA_PAGER` set, the text goes to your pager first and the question is asked after
      it.
  - The trust diff `sync` asks about and the trash's restore plan open the same way before their
    picker: `q  continue to: <question>` closes the viewer and opens it; Ctrl+C ends the command
    without asking.
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
  target, version, previous, source, file, era}`.
  - `previous` is the version the scope declared before, `""` for none; `era` is the `since` of the
    [era](../guides/target-specs.md#eras) the files are written for; `file` is the `project.yml` or
    `config.yaml` it goes in.
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
    `profile:user:<name>`); `list --plain` rows carry the two just before `ref`, which a final `update-available` (or `-`) follows.
  - `list --json` has `notices` (`[]` when none), as `--plain` has `notice` rows.
- `sync --json`:
  - Top level: `scope`, `root`, `dry_run`, `in_sync`, `trust`, `pins`, `removals`, `rewrites`,
    `equivalent_commands`, and the plan's `modules`, `steps`, `findings` and `notices`.
    - `rewrites` (`[]` when none): one `{module, target, from, to}` per module whose files the
      sync rewrites for another [era](../guides/target-specs.md#eras) of a target, `from` and `to`
      the eras' `since`.
    - `equivalent_commands`: in a dry run that is not in sync, the command that applies the plan,
      with the `--trust=` kinds it needs off a terminal, and `-y` when the plan removes files it
      lists in full (not while a module's new version is unread, whose removals the run lists and
      asks about); `[]` otherwise.
    - That `-y` confirms whatever the run it starts removes, which differs from the dry run's list
      when a source or `project.yml` changes between them.
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
    - `pre_approved` (`[]` when none): `kind` (`skill`, `command` or `agent`), `name` and `tools`:
      what a component's `allowed-tools` pre-approve that the version installed did not (all of
      them with none to compare).
    - `hooks`: `kind` (`install`, `runtime` or `mcp_server`), `target`, `event`, `detail` and
      `change`: `added`, `changed` (an install hook whose script file changed), `unchanged` or
      `removed`; `consent_again: true` on an `unchanged` runtime hook or agent MCP server of a
      version other than the one installed, which lola installed under a consent that covered
      that version and that has no consent recorded for the new one, so the run asks again
      (absent otherwise, a hook declined before included).
  - `trust.replacements` (`[]` when none): per project profile an activation puts in place of
    your active namesake, `profile`, `replaces` (the profile it replaces) and `modules` (those it
    changes); `status` is `changed` when only a replacement needs trust.
  - `pins`: `module`, `profile` (the project profile the pin is written into; absent for the
    manifest's), `ref`, `commit`, `digest`, `previous_digest`, `reason` (`unpinned` or
    `update`), `written` (false in a dry run).
  - `removals`: `module`, `targets`, `whole_module`, `paths` (each `target` and `path`; for a JSON
    member lola wrote, such as a server in `.mcp.json`, `path` is the file, a space, then the
    member's key: `.mcp.json /mcpServers/gh`), `scope`
    (`project` or `user`), and `why` when the run itself is not the reason. An install's removals
    at two scopes (`--scope both`) add `where` (`project <root>` or `user scope (<home>)`), which
    the human list prints after each block's reason.
  - Commits and digests are full.
  - With two scopes (`--scope both`), `sync --json` prints `{"scopes": [...]}`, one such
    document per scope in order; when a scope fails, the error document carries the scopes
    synced before it in `scopes` (`[]` when the first failed).
  - A refusal for lack of a terminal prints the changes on stderr first, except under `--json`.
  - `--plain` adds `pin` (module, digest, reason) and `remove` (module, target, path, scope) rows
    after the plan's:
    - Every `remove` row has the scope column (`project` or `user`), one scope or two, and a
      refusal that lists removals (`install`, `sync`, `update`, `lola profile`) prints the same
      rows on stderr.
    - A run's report adds a `remove` row for each settings hook group (its file, a space and its
      key, `.claude/settings.local.json /hooks/Stop`) or component file it deletes because it
      declined runtime hooks lola installed under an earlier consent; under `Removed` their line
      says `(runtime hooks declined)`, and under a dry run's `To remove`, which asked nothing,
      `(unless you consent to its runtime hooks)`.
    - `--json` has them as `delete` steps, not in `removals`.
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
  - `dry_run`, `nothing_to_migrate`, `python_home` (the home of the
    [Python lola](../guides/migrating.md), the implementation `lola migrate` converts from:
    `$LOLA_HOME` or `~/.lola`), and `trash` (`""` when nothing was put there).
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
