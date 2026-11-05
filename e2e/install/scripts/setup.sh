#!/usr/bin/env bash
# Builds the install suite's fixtures inside its container.
#
# setup.sh <v1> <v2>          /srv/git/kit.git: the kit fixture committed as v1 (tagged v1; main),
#                             then changed and committed as v2 (tagged v2; main stays at v1),
#                             refusing unless the commits are <v1> and <v2>; /work/kit.zip and
#                             /work/kit.tar.gz, made by the system's zip and tar, each holding
#                             the directory kit.
# setup.sh advance <v1> <v2>  moves main from <v1> to <v2>, as a push to the branch would.
#
# The container pins the commits' identity and dates, so the SHAs are the same on every run.
set -euo pipefail
repo=/srv/git/kit.git

if [[ "${1:-}" == advance ]]; then
  git -C "$repo" update-ref refs/heads/main "$3" "$2"
  echo "main at v2"
  exit 0
fi
v1="$1" v2="$2"

src="$(mktemp -d)"
cp -R /e2e/fixtures/kit/. "$src"
git -C "$src" init -q -b main
git -C "$src" add -A
git -C "$src" commit -q -m v1
git -C "$src" tag v1
printf -- '---\ndescription: Deploy the project\n---\nDeploy with kit v2.\n' >"$src/commands/deploy.md"
git -C "$src" commit -q -am v2
git -C "$src" tag v2
git -C "$src" reset -q --hard v1
mkdir -p /srv/git
git clone -q --bare "$src" "$repo"
rm -rf "$src"
for ref in v1 v2; do
  want="${!ref}" got="$(git -C "$repo" rev-parse "$ref^{commit}")"
  if [[ "$got" != "$want" ]]; then
    echo "error: kit.git's $ref is $got, not $want" >&2
    exit 1
  fi
done

(cd /e2e/fixtures && zip -qr /work/kit.zip kit)
tar -czf /work/kit.tar.gz -C /e2e/fixtures kit
echo "fixture ready"
