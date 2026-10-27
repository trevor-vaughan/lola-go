#!/usr/bin/env bash
# Runs the lola tidy container suite (e2e/tidy/tidy.venom.yml). It uses the lola migrate suite's
# image, built from the same recipe and named by the same tag (scripts/e2e-migrate.sh), so either
# suite builds it once for both; this suite needs only its git and python3. The project
# (/work/proj) and XDG_STATE_HOME (/home/tester/.local/state) are separate tmpfs mounts, so every
# move into the trash crosses file systems. The container runs as root, the uid that owns its
# image directories and both mounts, so git trusts the repository, with no network, no
# capabilities and no privilege gain, and mounts a copy of e2e/tidy made under $OUT. Its main
# process sleeps; the suite execs into it. It needs podman (or docker), venom and the network on the first run, which
# builds the image. It skips, saying why, when no container runtime is on PATH. MODE=human
# (default) streams venom's output; MODE=llm prints only failures, or one ok line. Artifacts go
# to $TEST_DIR/e2e-tidy.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MODE="${MODE:-human}"
OUT="${TEST_DIR:-$ROOT/.test-output}/e2e-tidy"
PY_LOLA_REPO="${PY_LOLA_REPO:-https://github.com/LobsterTrap/lola}"
PY_LOLA_COMMIT="${PY_LOLA_COMMIT:-91a28855fdae91c3ec07fdda44f331b10eb8c8c0}"
# The base image is pinned by the digest of its multi-platform index.
BASE_DIGEST=sha256:70729b46c69b4f1e97c4822c1af3df53a1476cf5ddc6c087c0c10bc3a5678c2f
BASE_IMAGE=docker.io/library/python:3.13-slim@$BASE_DIGEST

runtime=""
for rt in podman docker; do
  if command -v "$rt" >/dev/null 2>&1; then runtime="$rt"; break; fi
done
if [[ -z "$runtime" ]]; then
  echo "skipped: the lola tidy container suite needs podman or docker, and neither is on PATH"
  exit 0
fi
if ! command -v venom >/dev/null 2>&1; then
  echo "error: venom is not on PATH; install it from https://github.com/ovh/venom" >&2
  exit 1
fi
mkdir -p "$OUT/bin"

# The image is named after the base image's digest, the Python lola commit, the build script and
# the pinned packages, so a change to any of them rebuilds it.
recipe="$(cat "$ROOT/e2e/migrate/scripts/build-image.sh" "$ROOT/e2e/migrate/requirements.txt" | sha256sum | cut -c1-12)"
image="localhost/lola-migrate-e2e:py-$PY_LOLA_COMMIT-base-${BASE_DIGEST:7:12}-$recipe"
if ! "$runtime" image inspect "$image" >/dev/null 2>&1; then
  builder="lola-migrate-e2e-build-$$"
  "$runtime" run --name "$builder" -e PY_LOLA_REPO="$PY_LOLA_REPO" -e PY_LOLA_COMMIT="$PY_LOLA_COMMIT" \
    -v "$ROOT/e2e/migrate:/e2e:ro,z" "$BASE_IMAGE" bash /e2e/scripts/build-image.sh >"$OUT/image-build.log" 2>&1 \
    || { "$runtime" rm -f "$builder" >/dev/null 2>&1; echo "error: building $image failed; see $OUT/image-build.log" >&2; exit 1; }
  "$runtime" commit "$builder" "$image" >/dev/null
  "$runtime" rm "$builder" >/dev/null
fi

CGO_ENABLED=0 GOOS=linux go build -C "$ROOT" -trimpath -o "$OUT/bin/lola-go" ./cmd/lola

container="lola-tidy-e2e-$$"
cleanup() {
  local status=$?
  if ! "$runtime" rm -f "$container" >"$OUT/cleanup.log" 2>&1; then
    echo "warning: removing container $container failed; see $OUT/cleanup.log" >&2
  fi
  exit "$status"
}
trap cleanup EXIT
# The run container mounts a copy of the suite, so its relabel (:z) never touches the
# checked-in e2e/tidy.
rm -rf "$OUT/suite"
cp -R "$ROOT/e2e/tidy" "$OUT/suite"
"$runtime" run -d --name "$container" --user 0:0 -w /work \
  --network none --cap-drop ALL --security-opt no-new-privileges \
  --tmpfs /work/proj --tmpfs /home/tester/.local/state \
  -e HOME=/home/tester -e GIT_CONFIG_GLOBAL=/dev/null -e GIT_CONFIG_SYSTEM=/dev/null \
  -e GIT_AUTHOR_NAME=lola-e2e -e GIT_AUTHOR_EMAIL=lola-e2e@invalid \
  -e GIT_COMMITTER_NAME=lola-e2e -e GIT_COMMITTER_EMAIL=lola-e2e@invalid \
  -v "$OUT/suite:/e2e:ro,z" -v "$OUT/bin:/opt/lola-go:ro,z" \
  "$image" sleep infinity >/dev/null

args=(run "$ROOT/e2e/tidy/tidy.venom.yml" --var runtime="$runtime" --var container="$container" --output-dir "$OUT")
if [[ "$MODE" == "llm" ]]; then
  if venom "${args[@]}" >"$OUT/venom.log" 2>&1; then
    echo "ok: lola tidy container suite passed (log: $OUT/venom.log)"
  else
    pattern='FAIL|Failure|ShouldContain|ShouldEqual|ShouldNot|Error'
    if grep -qaE "$pattern" "$OUT/venom.log"; then
      grep -aE "$pattern" "$OUT/venom.log" | head -60
    else
      tail -40 "$OUT/venom.log"
    fi
    exit 1
  fi
else
  venom "${args[@]}"
fi
