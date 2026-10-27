#!/usr/bin/env bash
# Builds the market suite's fixtures inside its container, then starts the HTTPS git server
# (e2e/common/tls.sh): lola maps a catalog's git entry only when it is https://.
#
# setup.sh <lint v1> <lint v2> <market>
#
#   /work/mk               a copy of the folder marketplace mine (.claude-plugin/marketplace.json,
#                          with the relative entry ./kit and the git entry lint, from
#                          https://git.lola.test:8443/lint.git), which M8 edits
#   /work/other-kit        a copy of another module named kit
#   /srv/git/lint.git      the lint fixture committed as v1 (tagged v1), then changed and
#                          committed as v2; main at v2. The catalog pins lint at v1.
#   /srv/git/market.git    the Python lola's YAML catalog general.yml, one commit on main
#
# It refuses unless each commit is the one given. The container pins the commits' identity and
# dates, so the SHAs are the same on every run.
set -euo pipefail
if [[ $# -ne 3 ]]; then
  echo "usage: setup.sh <lint v1> <lint v2> <market>" >&2
  exit 2
fi
mkdir -p /srv/git
for name in lint market; do
  src="$(mktemp -d)"
  cp -R "/e2e/fixtures/$name/." "$src"
  git -C "$src" init -q -b main
  git -C "$src" add -A
  git -C "$src" commit -q -m "$name"
  if [[ "$name" == lint ]]; then
    git -C "$src" tag v1
    printf -- '---\ndescription: Lint the project\n---\nLint with lint v2.\n' >"$src/commands/lint.md"
    git -C "$src" commit -q -am v2
  fi
  git clone -q --bare "$src" "/srv/git/$name.git"
  rm -rf "$src"
done
for check in "lint v1 $1" "lint main $2" "market main $3"; do
  read -r name rev want <<<"$check"
  got="$(git -C "/srv/git/$name.git" rev-parse "$rev^{commit}")"
  if [[ "$got" != "$want" ]]; then
    echo "error: $name.git's $rev is $got, not $want" >&2
    exit 1
  fi
done
cp -R /e2e/fixtures/mk /work/mk
cp -R /e2e/fixtures/other-kit /work/other-kit
bash /e2e-common/tls.sh >/dev/null
echo "fixture ready"
