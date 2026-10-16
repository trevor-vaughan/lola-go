#!/usr/bin/env bash
# Scenario B: a Python home with only a user-scope OpenCode install, which migrates whole.
set -euo pipefail
export HOME=/home/b
mkdir -p "$HOME"
lola mod add /e2e/fixtures/review
lola install review -a opencode --scope user
echo "python lola installed"
