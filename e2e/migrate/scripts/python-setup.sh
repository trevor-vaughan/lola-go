#!/usr/bin/env bash
# Scenario A: the Python lola installs fixture modules from a folder, a git server and a zip
# into /work/proj (review for claude-code, gemini-cli and opencode; kit for claude-code and the
# unmappable cursor, so neither of kit's records migrates) and at user scope, and one from a
# folder with content_dirname "/" beside a module/ directory.
# The git server (gitserve.py on 127.0.0.1:8765) is the container's main process.
set -euo pipefail
fx=/e2e/fixtures
mkdir -p /work/proj /work/userdir /srv/git
git init -q /work/tools-src && cp -r "$fx/tools/." /work/tools-src/
git -C /work/tools-src add -A && git -C /work/tools-src commit -qm tools
git clone -q --bare /work/tools-src /srv/git/tools.git
for _ in $(seq 50); do python3 -c 'import socket; socket.create_connection(("127.0.0.1", 8765))' 2>/dev/null && break; sleep 0.1; done
(cd "$fx" && zip -qr /work/kit.zip kit)
lola mod add "$fx/review"
lola mod add http://127.0.0.1:8765/tools.git
lola mod add /work/kit.zip
lola mod add "$fx/rooted" --module-content /
for a in claude-code gemini-cli opencode; do lola install review -a "$a" /work/proj; done
for a in claude-code cursor; do lola install kit -a "$a" /work/proj; done
lola install rooted -a claude-code /work/proj
(cd /work/userdir && lola install tools -a claude-code --scope user)
echo "python lola installed"
