#!/usr/bin/env bash
# Builds the team suite's fixtures inside its container, then starts the HTTPS git server.
#
# setup.sh <kit v1> <kit v2> <lint> <fmt> <profiles> <team main> <team pin> <team drop>
#          <team script> <team script edit>
#
#   /srv/git/kit.git       the kit fixture committed as v1 (tagged v1), then changed and committed
#                          as v2 (tagged v2); main at v2
#   /srv/git/lint.git      the lint fixture, one commit on main
#   /srv/git/fmt.git       the fmt fixture, one commit on main
#   /srv/git/profiles.git  profiles/go.yml, listing fmt from https://git.lola.test:8443/fmt.git
#   /srv/git/team.git      a project: main holds .lola/project.yml, listing kit pinned at v1, and
#                          the project profile review, listing lint; the branch next adds the pin
#                          commit (kit pinned at v2) and then the drop commit (kit removed); the
#                          branch scripts adds, on main, the project's pre-install script
#                          ci/check.sh (it logs to /tmp/t16.log), then edits it
#
# It refuses unless every commit is the one given, then runs e2e/common/tls.sh. The container
# pins the commits' identity and dates, so the SHAs are the same on every run.
set -euo pipefail
if [[ $# -ne 10 ]]; then
  echo "usage: setup.sh <kit v1> <kit v2> <lint> <fmt> <profiles> <team main> <team pin> <team drop> <team script> <team script edit>" >&2
  exit 2
fi
fixtures=/e2e/fixtures
mkdir -p /srv/git

# repo <name> <dir>: a work tree holding a copy of <dir>, committed once on main.
repo() {
  local src
  src="$(mktemp -d)"
  cp -R "$2/." "$src"
  git -C "$src" init -q -b main
  git -C "$src" add -A
  git -C "$src" commit -q -m "$1"
  echo "$src"
}
# publish <name> <work tree>: the bare /srv/git/<name>.git cloned from it.
publish() {
  git clone -q --bare "$2" "/srv/git/$1.git"
  rm -rf "$2"
}
# expect <repo> <rev> <sha>
expect() {
  local got
  got="$(git -C "/srv/git/$1.git" rev-parse "$2^{commit}")"
  if [[ "$got" != "$3" ]]; then
    echo "error: $1.git's $2 is $got, not $3" >&2
    exit 1
  fi
}

src="$(repo v1 "$fixtures/kit")"
git -C "$src" tag v1
printf -- '---\ndescription: Deploy the project\n---\nDeploy with kit v2.\n' >"$src/commands/deploy.md"
git -C "$src" commit -q -am v2
git -C "$src" tag v2
publish kit "$src"
publish lint "$(repo lint "$fixtures/lint")"
publish fmt "$(repo fmt "$fixtures/fmt")"
publish profiles "$(repo profiles "$fixtures/profiles")"

src="$(repo main "$fixtures/team")"
git -C "$src" checkout -q -b next
cp "$fixtures/team-pin.yml" "$src/.lola/project.yml"
git -C "$src" commit -q -am "pin kit at v2"
cp "$fixtures/team-drop.yml" "$src/.lola/project.yml"
git -C "$src" commit -q -am "drop kit"
git -C "$src" checkout -q -b scripts main
mkdir "$src/ci"
printf '#!/bin/sh\necho "check $LOLA_MODULE_NAME $LOLA_ASSISTANT" >> /tmp/t16.log\n' >"$src/ci/check.sh"
chmod 755 "$src/ci/check.sh"
cp "$fixtures/team-script.yml" "$src/.lola/project.yml"
git -C "$src" add -A
git -C "$src" commit -q -m "check every install"
printf 'echo "and its targets" >> /tmp/t16.log\n' >>"$src/ci/check.sh"
git -C "$src" commit -q -am "check more"
git -C "$src" checkout -q main
publish team "$src"

expect kit v1 "$1"
expect kit v2 "$2"
expect lint main "$3"
expect fmt main "$4"
expect profiles main "$5"
expect team main "$6"
expect team next~1 "$7"
expect team next "$8"
expect team scripts~1 "$9"
expect team scripts "${10}"

bash /e2e-common/tls.sh >/dev/null
echo "fixture ready"
