# Decisions

Where this build follows or departs from the upstream ADRs, and why. The list mirrors §13 of
`docs/superpowers/specs/2026-10-08-lola-go-mvp-design.md`.

| Decision | Upstream position | Why |
|---|---|---|
| Client namespaces are routed per target. | The accepted Agent Plugins ADR feeds every namespace to every target. | A Claude-specific agent must not reach OpenCode or Gemini. (Applies once namespaces land; see [roadmap](roadmap.md).) |
| Destinations are written through symlinks (directory and file) and shown as `path (via symlink → /resolved)`. | ADR #239 and Python [#226](https://github.com/LobsterTrap/lola/issues/226) reject any path traversing a symlink. | Dotfile-farm users depend on it. Conflict detection plus the plan display bound the risk (see the security model in [architecture](architecture.md)). Two destinations that resolve to the same real file are refused. |
| The cache lives under XDG, not `~/.lola/modules/`. | ADR #245 places it at `~/.lola/modules/`. | lola follows the XDG base-directory layout: `$XDG_CACHE_HOME/lola/modules/`. Installs run from their own module copies, so cached content can always be fetched again. |
| `lock.json` is gitignored. | — | It records machine-specific facts (resolved symlinks, digests); `project.yml` is the shareable intent. |
| MCP server names are not rewritten. | — | Rewriting breaks `mcp__<server>__<tool>` references in skills; ownership comes from the ledger. |
| Python-written managed sections are not migrated or cleaned. | — | lola cannot tell which edits were the Python tool's; it only manages what its own ledger recorded. |
| The `git` CLI is used rather than a Go git library. | — | Credential helpers and SSH agents keep working. (Git sources are planned.) |
| Modules ship always-on context as `rules/*.md`, delivered through each assistant's native loading (Claude `.claude/rules/`); lola never edits `CLAUDE.md`, `GEMINI.md` or `AGENTS.md`. | ADR #242 ships `INSTRUCTIONS.md` through `@` pointer blocks in `CLAUDE.md`/`GEMINI.md`; Python installs a module's `AGENTS.md`. | Those files belong to the user. A lola-created `CLAUDE.md` also makes Claude Code stop reading an existing `AGENTS.md`. "Rules" says what the files do; `INSTRUCTIONS.md` reads like install instructions. |
| Python CLI compatibility is not a goal. | The go-migration ADR asks for identical commands. | The rewrite is an argument for a better UX. For the same reason there is no `mod` group: ADR #245 keeps one for compatibility, and nothing here needs it. |
| What lola writes never depends on the installed assistant's version; the version only adds findings. | — | Teammates on different versions would otherwise rewrite each other's committed files on every render. |
| Each target lists what it can express (capability tables) and reports what it loses as findings; one artifact a target cannot use is skipped, not the whole module. | APM drops unsupported content mostly silently and handles each target ad hoc. | Authors see every target's gaps with `lola check`; CI enforces them with `--strict`. |
