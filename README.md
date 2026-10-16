# lola

lola is a package manager for AI-assistant context: skills, slash commands, subagents, MCP
servers and always-on rules. You install a module once; lola renders it into each
assistant's native layout and records exactly what it wrote, so it can render again or
uninstall cleanly, without touching anything it did not write.

This is a Go rewrite of [LobsterTrap/lola](https://github.com/LobsterTrap/lola). It is
deliberately not command-compatible with the Python CLI; [docs/decisions.md](docs/decisions.md)
explains why.

## What works today

- **Assistants:** Claude Code, at project scope or user scope (`--user`).
- **Modules:** local folders in lola's native layout, with an optional `lola.yml`.
- **Safety:** lola only overwrites or deletes files it wrote and nobody has changed, unless
  you pass `--force`; an existing file identical to what lola would write is adopted, not
  rewritten. JSON files keep their comments; a file lola cannot parse is never rewritten.
  lola never edits your `CLAUDE.md` or `AGENTS.md`, and a module's install hooks run only
  with your consent.

Gemini CLI, OpenCode, git and archive sources, plugin formats, marketplaces, profiles and
templating are planned: see [docs/roadmap.md](docs/roadmap.md).

## Install

You need Go 1.26 or later, [Task](https://taskfile.dev), and git.

```sh
git clone https://github.com/trevor-vaughan/lola-go && cd lola-go
task build          # writes bin/lola
```

## Quickstart

```sh
lola init my-module                         # lola.yml, one skill, one command
mkdir my-project && cd my-project
lola install ../my-module -t claude-code    # renders into .claude/, records .lola/project.yml
lola list                                   # my-module  claude-code  ✓ ok
# edit ../my-module, then pick up the change:
lola install ../my-module
lola uninstall my-module -y                 # removes exactly what lola wrote
```

## Learn more

- `lola help` prints concise help on a terminal; piped (`lola help | less`) it prints the
  full reference, including every alias.
- [docs/usage.md](docs/usage.md): commands, scopes, files and the output contract.
- [docs/architecture.md](docs/architecture.md): how lola works, for contributors.
- [docs/decisions.md](docs/decisions.md) and [docs/roadmap.md](docs/roadmap.md): where this
  build departs from the upstream ADRs, and what comes next.
