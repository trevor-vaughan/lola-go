#!/usr/bin/env bash
# Checks the known marketplaces lola ships (internal/market/known.yaml, docs/maintainers/known-markets.md)
# against the network. For every entry it prints the upstream head, whether that differs from the
# pinned commit, and proves the pin still works: the source is a git repository, and
# `lola market --enable <id> -n -y` fetches the pinned commit and reads its catalog, in a
# throwaway HOME and XDG_* so no real configuration is touched. A moved head is a note, not a
# failure: whether to re-pin is the maintainer's call. A failed check is a failure.
# Needs network, git, jq, timeout (each network call is bounded). Exits 1 if any entry fails. Usage: known-markets-refresh.sh <lola>
set -uo pipefail

lola="${1:?usage: known-markets-refresh.sh <lola>}"
lola="$(realpath "$lola")"

for tool in git jq timeout; do
  command -v "$tool" >/dev/null || {
    echo "error: $tool is required and was not found on PATH" >&2
    exit 1
  }
done

home="$(mktemp -d "${TMPDIR:-/tmp}/lola-known.XXXXXX")"
trap 'rm -rf "$home"' EXIT
export HOME="$home" XDG_CONFIG_HOME="$home/config" XDG_DATA_HOME="$home/data" \
  XDG_CACHE_HOME="$home/cache" XDG_STATE_HOME="$home/state"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_TERMINAL_PROMPT=0

entries="$("$lola" market --known --json | jq -r '.known[] | [.id, .source, .commit] | @tsv')" || {
  echo "error: cannot list the known marketplaces with $lola" >&2
  exit 1
}
[[ -n "$entries" ]] || {
  echo "error: this lola ships no known marketplaces; nothing to refresh" >&2
  exit 1
}

failed=0
while IFS=$'\t' read -r id source pin; do
  echo "== $id ($source)"
  head="$(timeout 60 git ls-remote "$source" HEAD | cut -f1)"
  if [[ ! "$head" =~ ^[0-9a-f]{40}$ ]]; then
    echo "  FAIL not a reachable git repository (git ls-remote found no HEAD)"
    failed=1
    continue
  fi
  echo "  pin:  $pin"
  echo "  head: $head"
  if [[ "$head" == "$pin" ]]; then
    echo "  pin is the head"
  else
    echo "  note: the head has moved on; review the changes before re-pinning"
  fi
  if out="$(timeout 300 "$lola" market --enable "$id" -n -y 2>&1)"; then
    echo "  ok:   ${out%%$'\n'*}"
  else
    echo "  FAIL the pinned catalog does not enable:"
    sed 's/^/        /' <<<"$out"
    failed=1
  fi
done <<<"$entries"

exit "$failed"
