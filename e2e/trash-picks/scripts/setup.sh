#!/usr/bin/env bash
# Builds the trash-picks suite's fixture inside its container.
#
# /srv/git/kit.git   skills alpha, bravo and charlie, committed once, on main.
# /work/a, /work/b   two clones of one repository, whose origin names the git server's app.git, so
#                    lola keys their picks as git:127.0.0.1:8765/app#. whichever path they sit at.
#                    /work/a also holds a stray command, .claude/commands/stray.md.
set -euo pipefail
repo=/srv/git/kit.git
src="$(mktemp -d)"

cp -R /e2e/fixtures/kit/. "$src"
git -C "$src" init -q -b main
git -C "$src" add -A
git -C "$src" commit -q -m kit
mkdir -p /srv/git
git clone -q --bare "$src" "$repo"
rm -rf "$src"

mkdir -p /work/a/.claude/commands
git -C /work/a init -q -b main
git -C /work/a remote add origin http://127.0.0.1:8765/app.git
printf 'App.\n' >/work/a/README
git -C /work/a add README
git -C /work/a commit -q -m app
git clone -q /work/a /work/b
git -C /work/b remote set-url origin http://127.0.0.1:8765/app.git
printf 'stray-body\n' >/work/a/.claude/commands/stray.md
echo "fixture ready"
