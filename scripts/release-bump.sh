#!/usr/bin/env bash
# Cuts release vX.Y.Z on HEAD (docs/release.md item 7): regenerates CHANGELOG.md with git-cliff,
# commits it as chore(release): vX.Y.Z, and tags that commit with an annotated tag whose message
# holds the release's section of the changelog. The version comes from the argument, or from a
# prompt whose default is what git-cliff derives from the Conventional Commits since the last
# release tag (cliff.toml's [bump]).
# Never pushes. Usage: release-bump.sh <git-cliff> [X.Y.Z]
set -euo pipefail

# Keep identical to tag_pattern in cliff.toml.
RELEASE_TAG_RE='^v[0-9]+\.[0-9]+\.[0-9]+$'

cliff="${1:?usage: release-bump.sh <git-cliff> [X.Y.Z]}"
version="${2:-}"

die() {
  echo "error: $1" >&2
  shift
  for hint in "$@"; do echo "  hint: $hint" >&2; done
  exit 1
}

# Return CHANGELOG.md to HEAD; the tree was clean on entry, so HEAD is the right state. A
# CHANGELOG.md that HEAD does not track is removed.
restore_changelog() {
  git reset -q HEAD -- CHANGELOG.md
  if git cat-file -e HEAD:CHANGELOG.md 2>/dev/null; then
    git checkout -q HEAD -- CHANGELOG.md
  else
    rm -f CHANGELOG.md
  fi
}

[[ -z "$(git status --porcelain)" ]] ||
  die "the work tree is not clean; the release goes on the commit every checklist item passed on" "git status"

last="$(git tag --merged HEAD --list 'v*' --sort=-v:refname | grep -E -m1 "$RELEASE_TAG_RE" || true)"

"$cliff" --config cliff.toml --unreleased --strip all | grep -q '^- ' ||
  die "no Conventional Commit since ${last:-the start of the history}; there is nothing to release" "task release:notes"

if [[ -z "$version" ]]; then
  suggested="$("$cliff" --config cliff.toml --bumped-version)"
  suggested="${suggested#v}"
  printf 'Last release: %s\n' "${last:-none}" >&2
  printf 'New version [%s]: ' "$suggested" >&2
  # A closed stdin fails here rather than releasing a version nobody chose.
  IFS= read -r reply ||
    die "no version given and stdin is closed" "task release:bump -- $suggested"
  version="${reply:-$suggested}"
fi
version="${version#v}"
[[ "v$version" =~ $RELEASE_TAG_RE ]] ||
  die "version \"$version\" is not X.Y.Z" "task release:bump -- 0.1.0"
! git rev-parse -q --verify "refs/tags/v$version" >/dev/null ||
  die "tag v$version already exists" "git show v$version"
if [[ -n "$last" ]]; then
  newest="$(printf '%s\n%s\n' "${last#v}" "$version" | sort -V | tail -n1)"
  [[ "$newest" == "$version" && "${last#v}" != "$version" ]] ||
    die "v$version is not after the last release, $last"
fi

notes="$("$cliff" --config cliff.toml --unreleased --tag "v$version" --strip all | sed "/./,\$!d")"

# The whole file is regenerated rather than prepended to: cliff.toml skips chore(release)
# commits, so a later full regeneration reproduces this same content.
if ! "$cliff" --config cliff.toml --tag "v$version" --output CHANGELOG.md; then
  restore_changelog
  die "git-cliff could not write CHANGELOG.md; it was restored"
fi
git add CHANGELOG.md
if ! git commit -q -m "chore(release): v$version"; then
  restore_changelog
  die "the release commit failed (a hook may have rejected it); CHANGELOG.md was restored"
fi

printf 'lola v%s\n\n%s\n' "$version" "$notes" | git tag -a --cleanup=verbatim "v$version" -F - ||
  die "the release commit $(git rev-parse --short HEAD) was created but tagging failed" \
    "drop it with git reset --keep HEAD~1, then run release:bump again"
echo "✓ released v$version: CHANGELOG.md committed and tagged on $(git rev-parse --short HEAD)"
echo "  review: git show v$version"
echo "  publish when ready: git push origin HEAD v$version"
