# Roadmap

The MVP is delivered in eight plans; each ships a working binary. The authoritative list is
§2 of `docs/superpowers/specs/2026-10-08-lola-go-mvp-design.md`.

1. **Done: core slice.** Folder sources, the cache, the plan/ledger engine, Claude Code at
   project and user scope, and the CLI framework (help, aliases, output contract, completion).
2. **Done: ADR alignment.** Per-project module copies, with `${PLUGIN_ROOT}` pointing at them
   (ADR #245); normalizing a module when it is added to the cache, with a `.lola-origin`
   record; the cache under `$XDG_CACHE_HOME`; install hooks that run after per-module consent
   (ADR #239 §5, [#42](https://github.com/LobsterTrap/lola/issues/42)); and module rules
   delivered natively, with `--no-rules`.
3. **Done: target framework.** One package per target behind a contract, findings with
   severities, capability tables, probing installed assistants, a conformance suite, and
   `lola check`.
4. **Done: Gemini CLI and OpenCode.** Both targets on the framework, with capability tables,
   probes and rules delivered through owned list members in their settings; a symlinked
   `.lola/plugin-data` is refused and a module copy that is not a directory is replaced.
5. **Sources and formats**, in three plans:
   - **5a. Done: sources.** Git (`--ref` for branches, tags and full SHAs), zip/tar archives
     and archive URLs
     (`docs/superpowers/specs/2026-10-09-plan-5a-sources-design.md`).
   - **5b. Formats**, designed in `docs/superpowers/specs/2026-10-09-plan-5b-formats-design.md`
     (which reworks MVP spec §5.2–§5.4), in two plans:
     - **5b.1. Done: module formats.** Claude Code plugins and Agent Plugins v1 packages
       (client namespaces routed per target), adapted once at `cache add` into a `components:`
       section of `lola.yml`, and `init --format agent-plugins` as the default.
     - **5b.2. Done: Claude Code runtime hooks.** A module's runtime hooks rendered into
       `.claude/settings.local.json` (`~/.claude/settings.json` at user scope) only under the
       user's recorded consent, with `CLAUDE_PLUGIN_ROOT` and `CLAUDE_PLUGIN_DATA` supplied,
       and skipped when git could commit the settings file.
   - **5c. Done: interactive install flow.** `lola install` on a terminal walks through scope,
     modules (with Add a source…), assistants and components, then a summary with the
     equivalent commands and a review menu; no silent default scope (`--scope`,
     `default_scope`); remembered exclusions in `selections.yaml`, written by the pickers and
     by `--skip`/`--only`/`--unskip` and honored by `render` and `check`
     (`docs/superpowers/specs/2026-10-10-plan-5c-install-flow-design.md`).
6. **`sync`, profiles, marketplaces and `tidy`**, one plan each, in this order:
   - **6a. `lola sync`** with content pins in the manifest and trust for a pulled
     `project.yml`, delivered as 6a.1 pins (done: hardened manifest and ledger loading, pins
     written by `install`, machine-independent digests, `render` from the version installed),
     6a.2 trust (done: a per-project trust record of what lola wrote to `project.yml`,
     refusals of entries a pull changed, canonical local locations, private run staging, and
     `render` refusing a tracked `lock.json`), 6a.3a the command (done: `lola sync` with the
     trust diff, `--trust`, containment of local sources and confirmed removals), and 6a.3b
     sync everywhere and the UX audit (done: names limit a sync, a per-entry trust picker,
     `render` on the pin leaving untrusted modules out, the refusals' hints switched to
     `lola sync` with the scope, `list` showing modules not installed, one refusal for every
     missing answer, a diff that scales, dry runs ending with the command that applies them,
     pending post-install hooks, moved projects, accessible-mode Ctrl+C)
     (`docs/superpowers/specs/2026-10-12-plan-6a-sync-design.md`).
   - **6b. Profiles:** stackable named sets of modules
     ([#223](https://github.com/LobsterTrap/lola/issues/223)), delivered as 6b.1 a scope for
     every command and the pager, 6b.2a layers, 6b.2b `lola profile`, 6b.3a project profiles
     and 6b.3b remote profiles (done: `--add`, `--update` and `--remove`, `extends:`, and the
     profiles lock) (`docs/superpowers/specs/2026-10-14-plan-6b-profiles-design.md`).
   - **6c. Done: marketplaces.** `lola market` and `@market/module`, reading the Claude marketplace
     JSON catalog format, and the Python lola's YAML catalogs for migrated users, delivered as
     6c.1 catalogs and `lola market` (done: both formats read into one model as a stranger's
     input, `--add`, `--update`, `--remove`, listing and showing, folder marketplaces, the markets
     lock, and the Python lola's `lola market` verbs refused with lola's form as the hint),
     6c.2 `@market/module` (done: install, `cache add` and `check` of an address, pins bound to a
     ref, archive sums, the redirect rule, `market:` provenance, the install pickers' marketplace
     groups, catalog search, `sync --update` following a catalog, and a relative entry's content
     lists installed exactly, recorded as `components:`) and 6c.3 `@` sources in files (done:
     every file refuses one before trust and network, coached per entry, `lola install @m/x`
     replacing such an entry, and `lola migrate` registering the Python lola's marketplaces)
     (`docs/superpowers/specs/2026-10-21-plan-6c-marketplaces-design.md`).
   - **6d. `lola tidy`:** move what lola does not manage out of its destinations into the
     trash, delivered as 6d.1 the engine and the command (done: declared destinations per
     target, ownership from the ledgers, hard locks, git tracking asked per work tree, the
     keep-list, MCP entries backed up before the edit, a cross-device trash move that removes only
     what matches the copy, names and `--all` with every refusal coached) and 6d.2 (done: the
     move and keep pickers with the review's equivalent command, the `--protect`/`--unprotect`
     flag aliases, and the `task e2e -- tidy` container suite across two file systems)
     (`docs/superpowers/specs/2026-10-22-plan-6d-tidy-design.md`).
7. **Done: templating** ([#195](https://github.com/LobsterTrap/lola/issues/195)): opt-in per file,
   using Liquid templates through `osteele/liquid`, chosen over jet because a recursive jet
   template crashes the process; templates are untrusted, so rendering is bounded in time
   and output and sees plain data only ([analysis](template_engine_analysis.md)). Delivered as
   7.1 the sandbox, 7.2 whole-file `.md.liquid` templates, 7.3 partials and 7.4 per-target
   attribute files and module vars (done: `.targets.yaml`/`.targets.jsonc` merged per target and
   bound as `attrs`, `lola.yml` vars overridden by entries, trusted with them, recorded in
   `rendered_with`, and shown by `lola cache info`), then 7.5 relaxed the author rules (done: a
   frontmatter value refuses only when it renders a line break without `| json`, no key guard,
   missing `attrs` keys read as nil, numbers in attributes and vars, missing partials refused
   only when included, `..` in quoted strings, and the math filters)
   (`docs/superpowers/specs/2026-10-22-plan-7-templating-design.md`).
8. **Done: end-to-end suites, container, and the full documentation set.** Venom suites against
   the built binary in a container, delivered as 8.1 the shared runner (done: `task e2e [--
   <suite>...]` discovering every suite, one pinned image, a container with no network and no
   capabilities, the `migrate` and `tidy` suites moved onto it, and a confirmation refused under
   `--json` printing one line that carries its plan), 8.2 the `install` and `template` suites
   (done: the README quickstart run as written, every source kind, user scope, a pseudo-terminal,
   templates compared byte for byte, and every refusal's hint run), 8.3 the `team` and `market`
   suites (done: sync and trust from a clone, project, user and remote profiles, remote profiles
   over HTTPS at a host named by name with a test CA, and marketplaces end to end) and 8.4 the
   documentation set (done: `docs/usage.md` one section per command, the guides in
   `docs/guides/`, `docs/testing.md` and the release checklist `docs/release.md`)
   (`docs/superpowers/specs/2026-10-23-plan-8-e2e-docs-design.md`).

After the MVP:

- Generations and rollback: each install or render as an immutable rendered tree switched
  atomically, which would also carry per-version output for several installed versions of one
  assistant. Starts with a spike on whether the assistants follow symlinked files.
- WASM extension sandboxing ([#239](https://github.com/LobsterTrap/lola/pull/239)).
- `lola alias`; converting and exporting modules between formats.
- Signing ([#62](https://github.com/LobsterTrap/lola/issues/62),
  [#84](https://github.com/LobsterTrap/lola/issues/84),
  [#128](https://github.com/LobsterTrap/lola/issues/128)).
- Targets: Copilot, Cursor, OpenClaw, Codex, Antigravity, Windsurf, Kiro and pi (APM's range);
  editor-only assistants need a probe that does not rely on `PATH`.
- `install --pre-install` / `--post-install` flags.
- Release automation (goreleaser/CI), Windows, and `cache clean`.
