# Decisions

Where this build follows or departs from the upstream ADRs, and why. The list mirrors §13 of
`docs/superpowers/specs/2026-10-08-lola-go-mvp-design.md`.

| Decision | Upstream position | Why |
|---|---|---|
| Client namespaces are routed per target. | The accepted Agent Plugins ADR feeds every namespace to every target. | A Claude-specific agent must not reach OpenCode or Gemini. (Applies once namespaces land; see [roadmap](roadmap.md).) |
| Destinations are written through symlinks (directory and file) and shown as `path (via symlink → /resolved)`. | ADR #239 and Python [#226](https://github.com/LobsterTrap/lola/issues/226) reject any path traversing a symlink. | Dotfile-farm users depend on it. Conflict detection plus the plan display bound the risk (see the security model in [architecture](architecture.md)). Two destinations that resolve to the same real file are refused. |
| The cache lives under XDG, not `~/.lola/modules/`. | ADR #245 places it at `~/.lola/modules/`. | lola follows the XDG base-directory layout. Target: `$XDG_CACHE_HOME/lola/modules/` (Plan 2); see the transitional note below. |
| `lock.json` is gitignored. | — | It records machine-specific facts (resolved symlinks, digests); `project.yml` is the shareable intent. |
| MCP server names are not rewritten. | — | Rewriting breaks `mcp__<server>__<tool>` references in skills; ownership comes from the ledger. |
| Python-written managed sections are not migrated or cleaned. | — | lola cannot tell which edits were the Python tool's; it only manages what its own ledger recorded. |
| The `git` CLI is used rather than a Go git library. | — | Credential helpers and SSH agents keep working. (Git sources are planned.) |
| Modules ship always-on context as `rules/*.md`, delivered through each assistant's native loading (Claude `.claude/rules/`); lola never edits `CLAUDE.md`, `GEMINI.md` or `AGENTS.md`. (Plan 2; see the transitional note below.) | ADR #242 ships `INSTRUCTIONS.md` through `@` pointer blocks in `CLAUDE.md`/`GEMINI.md`; Python installs a module's `AGENTS.md`. | Those files belong to the user. A lola-created `CLAUDE.md` also makes Claude Code stop reading an existing `AGENTS.md`. "Rules" says what the files do; `INSTRUCTIONS.md` reads like install instructions. |
| Python CLI compatibility is not a goal. | The go-migration ADR asks for identical commands. | The rewrite is an argument for a better UX. The `mod` group exists only because ADR #245 keeps it. |

## Transitional (this build)

Plan 1 still differs from the target spec in these ways until Plan 2 lands:

- **IR is re-derived, not normalized at add.** The cache keeps a copy of the module's files
  (regular files, normalized modes) and lola re-reads the layout each time it renders.
  Plan 2 normalizes at add and records a `.lola-origin`.
- **`${PLUGIN_ROOT}` points into the cache** (a versioned directory under
  `$XDG_DATA_HOME/lola/modules/`). Plan 2 adds per-project module copies and points it there.
- **Install hooks are listed, never run.** Plan 2 adds consent-gated execution.
- **Instructions use `INSTRUCTIONS.md` and a pointer block in `CLAUDE.md`.** Plan 2 replaces
  them with `rules/*.md` delivered to `.claude/rules/lola/<module>/`, plus `--no-rules`.
- **The cache is under XDG data** (`$XDG_DATA_HOME/lola/modules/`), because MCP servers run
  files from it through `${PLUGIN_ROOT}` and deleting it breaks them. Plan 2 moves it to
  `$XDG_CACHE_HOME` once module copies remove that dependency.

See [roadmap](roadmap.md) for the plan sequence.
