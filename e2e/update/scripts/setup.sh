#!/usr/bin/env bash
# Builds and moves the update suite's git fixture inside its container.
#
# setup.sh              /srv/git/pack.git: the pack fixture committed once, on main.
# setup.sh advance [v]  commits version v (v2 unless given) of pack's deploy command to main.
# setup.sh remove       deletes pack.git, so the next fetch of it fails.
# setup.sh pick         /srv/git/pick.git: commands deploy and review, committed once, on main.
# setup.sh pick-add     commits a third command, lint, to pick's main.
# setup.sh pick-rename  renames pick's command deploy to ship on main.
# setup.sh drop         /srv/git/drop.git: commands deploy and review, committed once, on main.
# setup.sh drop-rename  renames drop's command review to audit on main.
#
# The container pins the commits' identity and dates.
set -euo pipefail
repo=/srv/git/pack.git

case "${1:-}" in
  "")
    src="$(mktemp -d)"
    cp -R /e2e/fixtures/pack/. "$src"
    git -C "$src" init -q -b main
    git -C "$src" add -A
    git -C "$src" commit -q -m v1
    mkdir -p /srv/git
    git clone -q --bare "$src" "$repo"
    rm -rf "$src"
    echo "fixture ready"
    ;;
  advance)
    v="${2:-v2}"
    src="$(mktemp -d)"
    git clone -q "$repo" "$src"
    printf -- '---\ndescription: Deploy the project\n---\nDeploy with pack %s.\n' "$v" >"$src/commands/deploy.md"
    git -C "$src" commit -q -am "$v"
    git -C "$src" push -q origin main
    rm -rf "$src"
    echo "main advanced"
    ;;
  remove)
    rm -rf "$repo"
    echo "pack.git removed"
    ;;
  pick)
    src="$(mktemp -d)"
    mkdir -p "$src/commands"
    printf 'Deploy with pick.\n' >"$src/commands/deploy.md"
    printf 'Review with pick.\n' >"$src/commands/review.md"
    git -C "$src" init -q -b main
    git -C "$src" add -A
    git -C "$src" commit -q -m v1
    mkdir -p /srv/git
    git clone -q --bare "$src" /srv/git/pick.git
    rm -rf "$src"
    echo "pick ready"
    ;;
  pick-add)
    src="$(mktemp -d)"
    git clone -q /srv/git/pick.git "$src"
    printf 'Lint with pick.\n' >"$src/commands/lint.md"
    git -C "$src" add commands/lint.md
    git -C "$src" commit -q -m lint
    git -C "$src" push -q origin main
    rm -rf "$src"
    echo "pick advanced"
    ;;
  pick-rename)
    src="$(mktemp -d)"
    git clone -q /srv/git/pick.git "$src"
    git -C "$src" mv commands/deploy.md commands/ship.md
    git -C "$src" commit -q -m rename
    git -C "$src" push -q origin main
    rm -rf "$src"
    echo "pick renamed"
    ;;
  drop)
    src="$(mktemp -d)"
    mkdir -p "$src/commands"
    printf 'Deploy with drop.\n' >"$src/commands/deploy.md"
    printf 'Review with drop.\n' >"$src/commands/review.md"
    git -C "$src" init -q -b main
    git -C "$src" add -A
    git -C "$src" commit -q -m v1
    mkdir -p /srv/git
    git clone -q --bare "$src" /srv/git/drop.git
    rm -rf "$src"
    echo "drop ready"
    ;;
  drop-rename)
    src="$(mktemp -d)"
    git clone -q /srv/git/drop.git "$src"
    git -C "$src" mv commands/review.md commands/audit.md
    git -C "$src" commit -q -m rename
    git -C "$src" push -q origin main
    rm -rf "$src"
    echo "drop renamed"
    ;;
  *)
    echo "usage: setup.sh [advance [v]|remove|pick|pick-add|pick-rename|drop|drop-rename]" >&2
    exit 2
    ;;
esac
