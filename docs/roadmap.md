# Roadmap

The MVP is delivered in seven plans; each ships a working binary. The authoritative list is
§2 of `docs/superpowers/specs/2026-10-08-lola-go-mvp-design.md`.

1. **Done: core slice.** Folder sources, the cache, the plan/ledger engine, Claude Code at
   project and user scope, and the CLI framework (help, aliases, output contract, completion).
2. **Done: ADR alignment.** Per-project module copies, with `${PLUGIN_ROOT}` pointing at them
   (ADR #245); normalizing a module when it is added to the cache, with a `.lola-origin`
   record; the cache under `$XDG_CACHE_HOME`; install hooks that run after per-module consent
   (ADR #239 §5, [#42](https://github.com/LobsterTrap/lola/issues/42)); and module rules
   delivered natively, with `--no-rules`.
3. **Gemini CLI and OpenCode** targets.
4. **Sources and formats.** Git (`--ref` for branches, tags and full SHAs), zip/tar archives
   and archive URLs, `.claude-plugin/plugin.json` (with runtime hooks), and Agent Plugins v1
   (namespaces, untargeted packages). `init --format` defaults to `agent-plugins`.
5. **Marketplaces, `sync` and profiles.** `lola market`, `@market/module`, `lola sync`, and
   stackable named sets of modules ([#223](https://github.com/LobsterTrap/lola/issues/223)).
6. **Templating** ([#195](https://github.com/LobsterTrap/lola/issues/195)): opt-in per file,
   using the jet engine, which needs dependency vetting before the plan starts.
7. **End-to-end suites, container, and the full documentation set.** Venom suites against the
   built binary in a container.

After the MVP:

- WASM extension sandboxing ([#239](https://github.com/LobsterTrap/lola/pull/239)).
- `lola alias`; converting and exporting modules between formats.
- Signing ([#62](https://github.com/LobsterTrap/lola/issues/62),
  [#84](https://github.com/LobsterTrap/lola/issues/84),
  [#128](https://github.com/LobsterTrap/lola/issues/128)).
- Targets: Copilot, Cursor, OpenClaw, Codex, Antigravity.
- The Claude marketplace JSON catalog format.
- `install --pre-install` / `--post-install` flags.
- Release automation (goreleaser/CI), Windows, and `cache clean`.
