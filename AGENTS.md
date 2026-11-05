# AGENTS.md

Guidance for coding agents working on lola (Go).

## Commands

- `task build` -> `bin/lola`; `task test` (add `MODE=llm` for failures only); `task lint`;
  `task vuln`; `task cross` (Windows/macOS build + vet); `task check` runs all gates. Artifacts go to `.test-output/`.
  `task e2e [-- <suite>...]` runs the container suites under `e2e/` (podman or docker, venom,
  network); it is not part of `check`, and the release checklist requires it.

## Rules

- **Behavior lives in `internal/engine`**; `internal/cli` only adapts it. New flows get engine
  specs first.
- **Targets** are one package each under `internal/target/<name>`, listed in `internal/targets`.
  They render through `target.Admit` and their capability table; anything they cannot express
  is a `target.Finding`, never an error. Import boundaries are enforced by `depguard`.
- **Templates** render only through `internal/templating`; it alone imports liquid and reads no
  files.
- **No package-level mutable state.** Paths come from `paths.Resolve(getenv)` in `main`; cobra
  globals are set once behind `sync.Once`.
- **Errors coach.** Attach next steps with `errs.WithHints`; the CLI prints them as `hint:` lines
  or the `hints` JSON array.
- **Output.** Every command emits through `app.emit`; errors print through `writeError`, which
  adds an error's `JSONFields` beside `error` and `hints` (a confirmation refused under `--json`
  adds, through `planFields`, the plan it would have asked about (what `-n --json` prints, where
  the command has `-n`); the rules that follow apply to them). JSON keys are snake_case; arrays
  are never `null`. Human output may use `✓ ⚠ ✗ →`; color only
  via `style`. Author-chosen text reaches output only through `untrusted.Text` or
  `untrusted.CleanErr`, except JSON `steps[].path`, `steps[].member` and `steps[].value`, which
  carry what lola wrote, `lola tidy`'s JSON `path` and `trash`, which carry paths lola
  wrote, moved or found in its destinations, and `lola init`'s JSON `path`, the directory lola
  writes to, raw but for JSON's escaping of control characters.
- **Vocabulary.** User-facing text (messages, hints, help, docs) says "cache", never "store" or
  "origin" (ADR #245). Internal identifiers (package `store`, `store.Origin`) keep their names.
- **Aliases** are additive and stable: never repurpose one, never collide with a sibling, use the
  canonical name in docs and JSON. The published table is pinned by a spec in
  `internal/cli/store_test.go`; update it deliberately.
- **Destructive commands** take `-y/--yes` and confirm on a terminal; `--force` overrides safety
  checks and answers the destructive question; it never answers a consent question (`--trust`,
  `--allow-hooks`, `--allow-runtime-hooks`).
- **Dependencies** are pinned and were vetted; adding one needs the `dependency-vetting` process
  and the maintainer's approval.

## Tests

- Ginkgo/Gomega. Every spec calls `testenv.Isolate()`.
- **No meta tests**: never test test files, source text, docs or repository layout, and never
  test test helpers. Convention specs that walk the live cobra tree are fine.
- Regression specs carry the names listed in the design spec §11; keep them.
