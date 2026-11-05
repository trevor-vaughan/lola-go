#!/usr/bin/env bash
# Builds the lola tidy suite's static fixture inside its container (no lola migrate run).
#
# /work/proj, a git work tree on the /work tmpfs: a committed .mcp.json and command, the module
# ship installed by lola-go, residue shaped as the Python lola leaves it (its module copy under
# .lola/modules, the files it wrote into .claude and a skill directory with a subdirectory in
# .gemini), an instruction file and a kept agent, and a .claude/skills link out of the project.
# /home/tester: a dotfiles-style ~/.claude link to ~/dotfiles/claude, inside $HOME, holding one
# stray command.
set -euo pipefail
lola=/opt/lola-go/lola-go
proj=/work/proj

mkdir -p "$proj/.claude/commands" "$proj/.claude/agents" "$proj/.lola/modules/kit/commands" /srv/shared-skills/shared
printf '{"mcpServers": {"legacy": {"command": "npx", "args": ["-y", "legacy-server"]}}}\n' >"$proj/.mcp.json"
printf 'Team command.\n' >"$proj/.claude/commands/team.md"
git -C "$proj" init -q
git -C "$proj" add .mcp.json .claude/commands/team.md
git -C "$proj" commit -q -m fixture

"$lola" install /e2e/fixtures/ship -C "$proj" -t claude-code >/dev/null

printf 'Deploy with kit.\n' >"$proj/.lola/modules/kit/commands/deploy.md"
printf 'Deploy with kit.\n' >"$proj/.claude/commands/deploy.md"
printf 'Scratch.\n' >"$proj/.claude/commands/tmp-1.md"
mkdir -p "$proj/.gemini/skills/lint/refs"
printf 'Lint with kit.\n' >"$proj/.gemini/skills/lint/SKILL.md"
printf 'Reference A.\n' >"$proj/.gemini/skills/lint/refs/a.md"
printf 'Scratch agent.\n' >"$proj/.claude/agents/scratch.md"
printf 'My notes.\n' >"$proj/.claude/agents/notes.md"
printf 'Instructions.\n' >"$proj/.claude/commands/CLAUDE.md"
printf 'Shared skill.\n' >/srv/shared-skills/shared/SKILL.md
ln -s /srv/shared-skills "$proj/.claude/skills"

mkdir -p "$HOME/dotfiles/claude/commands"
printf 'Stale.\n' >"$HOME/dotfiles/claude/commands/stale.md"
ln -s dotfiles/claude "$HOME/.claude"
echo "fixture ready"
