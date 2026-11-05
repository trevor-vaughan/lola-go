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
6. **Marketplaces, `sync` and profiles.** `lola market`, `@market/module`, `lola sync`, and
   stackable named sets of modules ([#223](https://github.com/LobsterTrap/lola/issues/223)).
7. **Templating** ([#195](https://github.com/LobsterTrap/lola/issues/195)): opt-in per file,
   using the jet engine, which needs dependency vetting before the plan starts.
8. **End-to-end suites, container, and the full documentation set.** Venom suites against the
   built binary in a container.

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
- The Claude marketplace JSON catalog format.
- `install --pre-install` / `--post-install` flags.
- Release automation (goreleaser/CI), Windows, and `cache clean`.
