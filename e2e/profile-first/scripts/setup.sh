#!/usr/bin/env bash
# Builds the profile-first suite's fixtures inside its container, then starts the HTTPS git server.
#
# setup.sh <review-kit> <skills> <skills next> <gopls v1.2> <gopls v2.0> <golang-lint> <team-kit>
#          <profiles> <acme>
#   /srv/git/review-kit.git        review-kit, one commit, tagged v1.4.0
#   /srv/git/cc-skills-golang.git  seven skills (golang-docs … golang-testing) on main; the branch
#                                  next changes golang-testing (setup.sh advance moves main there)
#   /srv/git/gopls.git             gopls tagged v1.2, then v2.0 (main)
#   /srv/git/golang-lint.git       golang-lint, one commit (served over HTTPS for the remote profile)
#   /srv/git/team-kit.git          team-kit, one commit
#   /srv/git/profiles.git          profiles/go.yml, the remote profile go, listing golang-lint pinned
#   /srv/git/acme.git              a team project: .lola/project.yml lists team-kit pinned, and the
#                                  project profile review (empty)
# setup.sh advance <skills> <skills next>  moves cc-skills-golang's main from <skills> to
#                                  <skills next>, as a push to the branch would.
#
# It refuses unless every commit is the one given, then runs e2e/common/tls.sh. The container
# pins the commits' identity and dates, so the SHAs are the same on every run.
set -euo pipefail
if [[ "${1:-}" == advance ]]; then
  git -C /srv/git/cc-skills-golang.git update-ref refs/heads/main "$3" "$2"
  echo "main at next"
  exit 0
fi
if [[ $# -ne 9 ]]; then
  echo "usage: setup.sh <review-kit> <skills> <skills next> <gopls v1.2> <gopls v2.0> <golang-lint> <team-kit> <profiles> <acme>" >&2
  exit 2
fi
fixtures=/e2e/fixtures
mkdir -p /srv/git

# repo <message> <dir>: a work tree holding a copy of <dir>, committed once on main.
repo() {
  local src
  src="$(mktemp -d)"
  cp -R "$2/." "$src"
  git -C "$src" init -q -b main
  git -C "$src" add -A
  git -C "$src" commit -q -m "$1"
  echo "$src"
}
# publish <name> <work tree>: the bare /srv/git/<name>.git cloned from it, every branch and tag.
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

src="$(repo v1.4.0 "$fixtures/review-kit")"
git -C "$src" tag v1.4.0
publish review-kit "$src"
src="$(repo skills "$fixtures/cc-skills-golang")"
git -C "$src" checkout -q -b next
printf -- '---\nname: golang-testing\ndescription: Go testing advice\n---\nGo testing advice, second edition.\n' \
  >"$src/skills/golang-testing/SKILL.md"
git -C "$src" commit -q -am "testing, second edition"
git -C "$src" checkout -q main
publish cc-skills-golang "$src"
src="$(repo v1.2 "$fixtures/gopls")"
git -C "$src" tag v1.2
printf -- '---\ndescription: gopls\n---\ngopls v2.0.\n' >"$src/commands/gopls.md"
git -C "$src" commit -q -am v2.0
git -C "$src" tag v2.0
publish gopls "$src"
publish golang-lint "$(repo golang-lint "$fixtures/golang-lint")"
publish team-kit "$(repo team-kit "$fixtures/team-kit")"
publish profiles "$(repo profiles "$fixtures/profiles")"
publish acme "$(repo acme "$fixtures/acme")"

expect review-kit v1.4.0 "$1"
expect cc-skills-golang main "$2"
expect cc-skills-golang next "$3"
expect gopls v1.2 "$4"
expect gopls v2.0 "$5"
expect golang-lint main "$6"
expect team-kit main "$7"
expect profiles main "$8"
expect acme main "$9"

bash /e2e-common/tls.sh >/dev/null
echo "fixture ready"
