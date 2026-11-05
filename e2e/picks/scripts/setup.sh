#!/usr/bin/env bash
# Builds and moves the picks suite's git fixture inside its container.
#
# setup.sh      /srv/git/kit.git: skills alpha (which mentions bravo), bravo and charlie, committed
#               once, on main.
# setup.sh add  commits a fourth skill, delta, to kit's main.
#
# The container pins the commits' identity and dates.
set -euo pipefail
repo=/srv/git/kit.git

case "${1:-}" in
  "")
    src="$(mktemp -d)"
    cp -R /e2e/fixtures/kit/. "$src"
    git -C "$src" init -q -b main
    git -C "$src" add -A
    git -C "$src" commit -q -m kit
    mkdir -p /srv/git
    git clone -q --bare "$src" "$repo"
    rm -rf "$src"
    echo "fixture ready"
    ;;
  add)
    src="$(mktemp -d)"
    git clone -q "$repo" "$src"
    mkdir -p "$src/skills/delta"
    printf -- '---\ndescription: Delta\n---\nDelta.\n' >"$src/skills/delta/SKILL.md"
    git -C "$src" add -A
    git -C "$src" commit -q -m delta
    git -C "$src" push -q origin main
    rm -rf "$src"
    echo "delta added"
    ;;
  *)
    echo "usage: setup.sh [add]" >&2
    exit 2
    ;;
esac
