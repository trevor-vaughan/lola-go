# lola

lola is a package manager for AI-assistant context: skills, slash commands, subagents, MCP
servers and always-on rules. You install a module once; lola renders it into each
assistant's native layout and records exactly what it wrote, so it can render again or
uninstall cleanly, without touching anything it did not write.

This is a Go rewrite of [LobsterTrap/lola](https://github.com/LobsterTrap/lola). It is
deliberately not command-compatible with the Python CLI; [docs/decisions.md](docs/decisions.md)
explains why. Coming from the Python lola? `lola migrate` installs what it installed and
moves its files to a trash you can restore from ([docs/usage.md](docs/usage.md#coming-from-the-python-lola)).

## What works today

- **Assistants:** Claude Code, Gemini CLI and OpenCode, at project scope or user scope
  (`--user`).
- **Modules:** in lola's native layout (with an optional `lola.yml`), Claude Code plugins and
  Agent Plugins packages, from a local folder, a git repository (`--ref` for a branch, tag or
  commit), or a zip or tar archive, local or downloaded.
- **Portability report:** `lola check ./my-module` shows what each assistant would drop,
  adapt or skip, without writing anything; `install` prints the same findings, and
  `--strict` makes them errors.
- **Safety:** lola only overwrites or deletes files it wrote and nobody has changed, unless
  you pass `--force`; an existing file identical to what lola would write is adopted, not
  rewritten. JSON files keep their comments; a file lola cannot parse is never rewritten.
  lola never edits your `CLAUDE.md`, `GEMINI.md` or `AGENTS.md`, and a module's install hooks
  run, and its Claude Code runtime hooks are installed, only with your consent.

Marketplaces, profiles and templating are planned: see
[docs/roadmap.md](docs/roadmap.md).

## Install

You need Go 1.26 or later, [Task](https://taskfile.dev), and git. `go.mod` pins toolchain
go1.26.9, which fixes a reachable standard-library vulnerability; Go fetches it automatically
unless `GOTOOLCHAIN=local` is set, as some distribution packages do (`go env -w
GOTOOLCHAIN=auto` re-enables it).

```sh
git clone https://github.com/trevor-vaughan/lola-go && cd lola-go
task build          # writes bin/lola
```

## Quickstart

```sh
lola init my-module                         # plugin.json, one skill, one command (Agent Plugins)
mkdir my-project && cd my-project
lola install ../my-module --scope project -t claude-code   # renders into .claude/, records .lola/project.yml
lola list                                   # my-module  claude-code  ✓ ok
# edit ../my-module, then pick up the change:
lola install ../my-module                   # no --scope: a terminal asks (nothing preselected); a script uses the .lola/ found
lola uninstall my-module -y                 # removes exactly what lola wrote
```

## Learn more

- `lola help` prints concise help on a terminal; piped (`lola help | less`) it prints the
  full reference, including every alias.
- [docs/usage.md](docs/usage.md): commands, scopes, files and the output contract.
- [docs/architecture.md](docs/architecture.md): how lola works, for contributors.
- [docs/decisions.md](docs/decisions.md) and [docs/roadmap.md](docs/roadmap.md): where this
  build departs from the upstream ADRs, and what comes next.
