# AGENTS.md

Guidance for coding agents working on lola (Go).

## Commands

- `task build` -> `bin/lola`; `task test` (add `MODE=llm` for failures only); `task lint`;
  `task vuln`; `task cross` (Windows/macOS build + vet); `task check` runs all gates. Artifacts go
  to `.test-output/`. `task e2e [-- <suite>...]` runs the container suites under `e2e/` (podman or
  docker, venom, network); it is not part of `check`, and the release checklist requires it.
  `task release:notes` and `task release:bump [-- X.Y.Z]` are the release steps
  (`docs/release.md`).

## Commits

- Subjects follow [Conventional Commits](https://www.conventionalcommits.org)
  (`feat(scope): …`, `fix: …`, `docs(…): …`); git-cliff turns them into a release's notes
  (`cliff.toml`), so write each subject as a release-note line.

## Rules

- **Behavior lives in `internal/engine`**; `internal/cli` only adapts it. New flows get engine
  specs first.
- **Targets** are YAML specs in `internal/targetspec/specs` (facts: layout, detection, probe,
  frontmatter, behaviors), read through `internal/target/policy` (lola's severities, guards and
  hints) and rendered by `internal/target/mech` mechanisms in the generic
  `internal/target/spec` target; `internal/targets` registers them. A tool whose mechanisms
  exist needs only a spec in `internal/targetspec/specs`, or a user's in
  `$XDG_CONFIG_HOME/lola/targets`; `targets.Load` builds the `Registry` the CLI passes down.
  They render through `target.Admit`; anything they cannot express is a `target.Finding`, never
  an error. Import boundaries are enforced by `depguard`.
- **Templates** render only through `internal/templating`; it alone imports liquid and reads no
  files.
- **No package-level mutable state.** Paths come from `paths.Resolve(getenv)` in `main`; cobra
  globals are set once behind `sync.Once`.
- **Errors coach.** Attach next steps with `errs.WithHints`; the CLI prints them as `hint:` lines
  or the `hints` JSON array.
- **Output.** Every command emits through `app.emit`; scope commands print their context line
  through `printContext` (stderr, human output, and under `--plain` a `context` row) and `app.emit`
  puts their `context` first in JSON; errors print through `writeError`, which adds an error's
  `JSONFields` beside `error` and `hints`
  (a confirmation refused under `--json` adds, through `planFields`, the plan it would have asked
  about (what `-n --json` prints, where the command has `-n`); the rules that follow apply to them).
  JSON keys are snake_case; arrays are never `null`. Human output may use `✓ ⚠ ✗ →`; color only via
  `style`. Author-chosen text reaches output only through `untrusted.Text` or `untrusted.CleanErr`,
  except JSON `steps[].path`, `steps[].member` and `steps[].value`, which carry what lola wrote,
  `lola tidy`'s JSON `path` and `trash`, which carry paths lola wrote, moved or found in its
  destinations, `lola cache clean`'s JSON `path`, which carries a path in its cache as it is on disk
  unless it holds control or format characters, which are replaced, and `lola init`'s JSON `path`,
  the directory lola writes to, raw but for JSON's escaping of control characters.
- **Vocabulary.** User-facing text (messages, hints, help, docs) says "cache", never "store" or
  "origin" (ADR #245). Internal identifiers (package `store`, `store.Origin`) keep their names.
- **Aliases** are additive and stable: never repurpose one, never collide with a sibling, use the
  canonical name in docs and JSON. The published table is pinned by a spec in
  `internal/cli/store_test.go`; update it deliberately (one recorded exception: `update`, see
  `docs/decisions.md`).
- **Destructive commands** take `-y/--yes` and confirm on a terminal; `--force` overrides safety
  checks and answers the destructive question; it never answers a consent question
  (`--trust=<any kind>`).
- **Dependencies** are pinned and were vetted; adding one needs the `dependency-vetting` process
  and the maintainer's approval.

## Tests

- Ginkgo/Gomega. Every spec calls `testenv.Isolate()`.
- **No meta tests**: never test test files, source text, docs or repository layout, and never
  test test helpers. Convention specs that walk the live cobra tree are fine.
- Regression specs carry the names listed in `docs/testing.md` ("Regression names"); keep them.

## Driving the TUIs

A plain `bash` tool has no TTY, so interactive commands take the static path (`lola install` with no
args just errors `needs a module or source`). That proves parsing, not the UI. Launch in `tmux`,
poll for a marker, send keys, read the pane. Assertions belong in specs and `--plain`/`--json` runs.

- **Gates.** Forms and questions: `interactive()` (`internal/cli/prompt.go:20`) needs stdin and stderr
  on a TTY; `LOLA_ACCESSIBLE=1` or `TERM=dumb` swaps them for numbered line prompts. Viewer/pager:
  `app.paging` (`internal/cli/pager.go:37`) needs stdout and stderr on a TTY and none of `--json`,
  `--plain`, `--no-pager`, `NO_PAGER`, `pager: false`, `LOLA_PAGER=cat`; it opens only when the output
  is at least as tall as the terminal, so use `-y 6` to force it.
- **Surfaces** (ready marker): `lola help <cmd>` / bare `lola` viewer (`lines N–M of T`); `lola install`
  form (`Step 1 of 5`, review `y apply · c change · n cancel`); scope pickers on `tidy`, `update`,
  `sync`, `repair`, `profile … --on/--switch/--move` (`<Verb> where?`); `trash` (`Restore or empty
  which?`); `cache clean` (`Remove these from your cache?`); `init` without `-y`; `profile <n> --edit
  <mod>` (`Step 1 of 3`); trust diff on `sync`/`update` (`Trust these changes?`); `uninstall --project`
  viewer before its question (`q continue:` footer). `uninstall`, `cache rm`, `info`, `cache add` and
  `market --add` with no args never go interactive; they error with hints.
- **Keys.** Pickers: `x` toggle, `ctrl+a` all/none, `/` filter, `Enter`, `esc` back, `q` quit.
  Confirms: `y`/`n`. Viewer: `j`/`k`, `g`/`G`, `/` then `Enter`, `n`/`N`, `q`. `ctrl+c` exits 0 with
  `nothing changed`. After `q` the viewer reprints its content to scrollback.

```bash
sudo dnf install -y tmux; task build
tmux new-session -d -s holder -x 80 -y 10 'sleep 3000'   # a dying TUI as last session kills the server
tmux new-session -d -s v -x 120 -y 30 -c "$PROJ" "bash -c 'env HOME=… XDG_…=… lola help install; echo EXIT=\$?; sleep 60'"
timeout 15 bash -c 'until tmux capture-pane -t v -p | grep -q "lines [0-9]"; do sleep 0.2; done'
tmux send-keys -t v / ; tmux send-keys -t v hook; tmux send-keys -t v Enter
tmux capture-pane -t v -p | grep -v '^$' | tail -6
tmux kill-session -t v; tmux kill-session -t holder
```

- **Gotchas.** Use `-x 120` or wider (at 80 the viewer footer drops `q quit`). Pass the isolated
  `HOME`/`XDG_*` inside the pane's `bash -c`; a running tmux server does not inherit your env.
  Kill by session name, never `pkill -f` (it matches your own shell). `lola trash --plain` rows start
  with a literal `run` column. `bin/lola` can be stale; `task build` refreshes it.
