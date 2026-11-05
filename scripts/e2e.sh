#!/usr/bin/env bash
# Runs the e2e container suites against the release-built binary: every directory e2e/<n>/ that
# holds <n>.venom.yml, or only the suites named as arguments. It needs podman (or docker), venom
# and, the first time, the network to build the image; it skips, saying why, when no container
# runtime is on PATH. A failing suite never stops the next; the run exits 1 if any failed.
#
# The image (python:3.13-slim pinned by digest, git, zip and the Python lola at a full-SHA
# commit, its packages pinned by hash) is built once by run and commit (e2e/image/build-image.sh
# says why) and named after every pin and a hash of the recipe, so a change to any rebuilds it.
# Each suite gets its own container: no network, no capabilities, no privilege gain, root inside
# (the uid owning its tmpfs mounts, so git trusts the repositories), and HOME and every XDG
# directory pinned under /home/tester. Five tmpfs mounts hold /work, /home/tester,
# /home/tester/.local/state (the trash, so a move to it from /work crosses file systems),
# /run/gitserve and /run/e2e-tls (mode 0700: the test CA of a suite that runs
# e2e/common/tls.sh, which dies with the container). It mounts read-only copies of the suite
# and e2e/common, and the binary this script builds, all in a mktemp -d directory it makes
# under $OUT_ROOT and removes on exit, so nothing in the source tree or already under $OUT_ROOT
# is mounted or relabeled. The Python lola's setup needs no capability (checked under
# --cap-drop ALL, Plan 8 8.1 task 1). The container's main process is the git server,
# e2e/common/gitserve.py; the suite execs into it.
#
# MODE=human (default) streams venom's output; MODE=llm prints one ok line per suite or its
# failures. Both end with a summary; a suite over the 90 s budget is marked slow. Artifacts go
# to $TEST_DIR (default .test-output)/e2e-<suite>/.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
MODE="${MODE:-human}"
BUDGET=90

if [[ "$MODE" != human && "$MODE" != llm ]]; then
  echo "error: MODE must be human or llm, not $MODE" >&2
  exit 1
fi

# The runner writes under the artifacts' root and mounts only the mktemp -d directories it makes
# there; the root may not be the repository, $HOME or anything above them.
OUT_ROOT="$(realpath -m -- "${TEST_DIR:-$ROOT/.test-output}")"
guarded=("$ROOT")
if [[ -n "${HOME:-}" ]]; then guarded+=("$(realpath -m -- "$HOME")"); fi
for dir in "${guarded[@]}"; do
  if [[ "$OUT_ROOT" == / || "$OUT_ROOT" == "$dir" || "$dir" == "$OUT_ROOT"/* ]]; then
    echo "error: TEST_DIR=${TEST_DIR:-$OUT_ROOT} would put e2e artifacts at or above $dir; use a directory inside the repository, such as .test-output" >&2
    exit 1
  fi
done

runtime=""
for rt in podman docker; do
  if command -v "$rt" >/dev/null 2>&1; then runtime="$rt"; break; fi
done
if [[ -z "$runtime" ]]; then
  echo "skipped: the e2e suites need podman or docker, and neither is on PATH"
  exit 0
fi
if ! command -v venom >/dev/null 2>&1; then
  echo "error: venom is not on PATH; install it from https://github.com/ovh/venom" >&2
  exit 1
fi

all=()
for dir in "$ROOT"/e2e/*/; do
  name="$(basename "$dir")"
  if [[ -f "$dir/$name.venom.yml" ]]; then all+=("$name"); fi
done
suites=("$@")
if [[ ${#suites[@]} -eq 0 ]]; then suites=("${all[@]}"); fi
for name in "${suites[@]}"; do
  found=false
  for known in "${all[@]}"; do
    if [[ "$name" == "$known" ]]; then found=true; break; fi
  done
  if ! "$found"; then
    echo "error: no e2e suite named $name; suites: ${all[*]}" >&2
    exit 1
  fi
done

# shellcheck source=SCRIPTDIR/../e2e/image/pins.env
source "$ROOT/e2e/image/pins.env"

# What the containers mount and the binary live in a directory this run makes and removes, so
# no directory already under $OUT_ROOT is mounted, relabeled or built into; the runner rewrites
# only its own logs there.
mkdir -p "$OUT_ROOT"
work="$(mktemp -d "$OUT_ROOT/e2e-run.XXXXXX")"
# Containers are removed by the ID their run wrote to a cidfile, never by name, so a name clash
# cannot remove a container this run did not create. builder_pid is the background run of the
# image build: cleanup stops it first, so it cannot create the builder, or recreate a mount
# source, after cleanup has looked.
builder_pid="" builder_cid="$work/builder.cid" suite_cid=""
cleanup() {
  local status=$? cid
  if [[ -n "$builder_pid" ]]; then
    kill "$builder_pid" 2>/dev/null || : # it may have ended already
    wait "$builder_pid" || : # its status is the interrupted build's, which this exit replaces
  fi
  for cid in "$builder_cid" "$suite_cid"; do
    if [[ -n "$cid" && -s "$cid" ]] && ! "$runtime" rm -f "$(<"$cid")" >/dev/null 2>&1; then
      echo "warning: removing container $(<"$cid") failed" >&2
    fi
  done
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT
# An interrupt exits through cleanup, which removes the build or suite container: the build's
# bash, the container's PID 1, ignores the Ctrl+C the runtime passes on, and a suite's container
# outlives venom.
trap 'exit 130' INT
trap 'exit 143' TERM

# shown names a path relative to the repository when it is inside it.
shown() { if [[ "$1" == "$ROOT"/* ]]; then echo "${1#"$ROOT"/}"; else echo "$1"; fi; }

repo="$(printf '%s' "$PY_LOLA_REPO" | sha256sum | cut -c1-12)"
recipe="$(cat "$ROOT/e2e/image/build-image.sh" "$ROOT/e2e/image/requirements.txt" "$ROOT/e2e/image/pins.env" | sha256sum | cut -c1-12)"
image="localhost/lola-e2e:py-$PY_LOLA_COMMIT-repo-$repo-base-${BASE_DIGEST:7:12}-$recipe"
if ! "$runtime" image inspect "$image" >/dev/null 2>&1; then
  # The build container keeps the default capabilities and the network: apt-get and git clone
  # need them.
  cp -R "$ROOT/e2e/image" "$work/image"
  log="$OUT_ROOT/image-build.log"
  # The build runs in the background so that wait, unlike a foreground command, returns at once
  # on an interrupt and its trap runs.
  # --sig-proxy=false: cleanup's kill ends the client at once rather than being passed on to the
  # build's bash, which as PID 1 ignores it; --stop-timeout 0: the rm -f by ID that follows kills
  # the container at once rather than waiting for that bash to stop.
  "$runtime" run --sig-proxy=false --stop-timeout 0 --cidfile "$builder_cid" -e PY_LOLA_REPO="$PY_LOLA_REPO" -e PY_LOLA_COMMIT="$PY_LOLA_COMMIT" \
    -v "$work/image:/e2e-image:ro,z" "$BASE_IMAGE" bash /e2e-image/build-image.sh >"$log" 2>&1 &
  builder_pid=$!
  built=true
  wait "$builder_pid" || built=false
  builder_pid=""
  if ! "$built" || ! "$runtime" commit "$(<"$builder_cid")" "$image" >>"$log" 2>&1; then
    echo "error: building $image failed; see $(shown "$log")" >&2
    exit 1
  fi
  "$runtime" rm "$(<"$builder_cid")" >/dev/null
  # podman deletes the cidfile with its container; docker leaves it.
  rm -f "$builder_cid"
fi

mkdir "$work/bin"
CGO_ENABLED=0 GOOS=linux go build -C "$ROOT" -trimpath -o "$work/bin/lola-go" ./cmd/lola

# run_suite runs one suite in its own container, its output in $OUT/venom.log, and returns
# venom's status.
run_suite() {
  # It runs as an if condition, where set -e is off, so every step checks its own status.
  local name="$1" OUT="$OUT_ROOT/e2e-$1" container copy version options rootless=false venom_version
  mkdir -p "$OUT" || return 1
  rm -f "$OUT/venom.log" || return 1
  copy="$(mktemp -d "$work/$name.XXXXXX")" || return 1
  cp -R "$ROOT/e2e/$name" "$copy/suite" || return 1
  cp -R "$ROOT/e2e/common" "$copy/common" || return 1
  version="$("$runtime" --version)" || return 1
  if [[ "$runtime" == podman ]]; then
    rootless="$("$runtime" info --format '{{.Host.Security.Rootless}}')" || return 1
  else
    options="$("$runtime" info --format '{{.SecurityOptions}}')" || return 1
    if [[ " ${options//[][]/ } " == *" name=rootless "* ]]; then rootless=true; fi
  fi
  venom_version="$(venom version)" || return 1
  printf 'runtime: %s\nrootless: %s\n%s\n' "$version" "$rootless" "$venom_version" \
    >"$OUT/versions.txt" || return 1
  container="lola-e2e-$name-$$" suite_cid="$copy/container.cid"
  if ! "$runtime" run -d --init --name "$container" --cidfile "$suite_cid" --user 0:0 -w /work \
    --network none --cap-drop ALL --security-opt no-new-privileges \
    --tmpfs /work --tmpfs /home/tester --tmpfs /home/tester/.local/state --tmpfs /run/gitserve \
    --tmpfs /run/e2e-tls:mode=0700 \
    -e HOME=/home/tester -e XDG_CONFIG_HOME=/home/tester/.config -e XDG_DATA_HOME=/home/tester/.local/share \
    -e XDG_CACHE_HOME=/home/tester/.cache -e XDG_STATE_HOME=/home/tester/.local/state \
    -e GIT_CONFIG_GLOBAL=/dev/null -e GIT_CONFIG_SYSTEM=/dev/null \
    -e GIT_AUTHOR_NAME=lola-e2e -e GIT_AUTHOR_EMAIL=lola-e2e@invalid \
    -e GIT_COMMITTER_NAME=lola-e2e -e GIT_COMMITTER_EMAIL=lola-e2e@invalid \
    -e "GIT_AUTHOR_DATE=2026-01-01T00:00:00+00:00" -e "GIT_COMMITTER_DATE=2026-01-01T00:00:00+00:00" -e NO_COLOR=1 \
    -v "$copy/suite:/e2e:ro,z" -v "$copy/common:/e2e-common:ro,z" -v "$work/bin:/opt/lola-go:ro,z" \
    "$image" python3 /e2e-common/gitserve.py /srv/git 8765 >"$OUT/venom.log" 2>&1; then
    echo "error: starting the container for $name failed; see $(shown "$OUT/venom.log")" >&2
    # run -d can create the container and then fail to start it.
    if [[ -s "$suite_cid" ]] && ! "$runtime" rm -f "$(<"$suite_cid")" >/dev/null 2>&1; then
      echo "warning: removing container $container failed" >&2
    fi
    suite_cid=""
    return 1
  fi
  local status=0
  # venom writes its own venom.log into --output-dir; its console output replaces it.
  if [[ "$MODE" == llm ]]; then
    venom run "$copy/suite/$name.venom.yml" --var runtime="$runtime" --var container="$container" \
      --output-dir "$OUT" >"$OUT/console.log" 2>&1 || status=$?
  else
    venom run "$copy/suite/$name.venom.yml" --var runtime="$runtime" --var container="$container" \
      --output-dir "$OUT" 2>&1 | tee "$OUT/console.log" || status=$?
  fi
  mv "$OUT/console.log" "$OUT/venom.log" || status=1
  if ! "$runtime" rm -f "$(<"$suite_cid")" >/dev/null 2>&1; then
    echo "warning: removing container $container failed" >&2
  fi
  suite_cid=""
  return "$status"
}

start=$SECONDS
passed=0 failed=0 rows=()
for name in "${suites[@]}"; do
  began=$SECONDS
  log="$(shown "$OUT_ROOT/e2e-$name/venom.log")"
  if run_suite "$name"; then
    status=ok passed=$((passed + 1))
    if [[ "$MODE" == llm ]]; then echo "ok: e2e $name passed in $((SECONDS - began))s (log: $log)"; fi
  else
    status=FAIL failed=$((failed + 1))
    # A suite that failed before its container started has no venom.log; its error is above.
    if [[ ! -f "$OUT_ROOT/e2e-$name/venom.log" ]]; then
      log=""
    elif [[ "$MODE" == llm ]]; then
      pattern='FAIL|Failure|ShouldContain|ShouldEqual|ShouldNot|Error'
      if grep -qaE "$pattern" "$OUT_ROOT/e2e-$name/venom.log"; then
        grep -aE "$pattern" "$OUT_ROOT/e2e-$name/venom.log" | head -60
      else
        tail -40 "$OUT_ROOT/e2e-$name/venom.log"
      fi
    fi
  fi
  rows+=("$status|$name|$((SECONDS - began))|$log")
done

width=0
for name in "${suites[@]}"; do width=$((${#name} > width ? ${#name} : width)); done
elapsed=$((SECONDS - start))
printf 'e2e: %d passed, %d failed in %dm%02ds\n' "$passed" "$failed" $((elapsed / 60)) $((elapsed % 60))
for row in "${rows[@]}"; do
  IFS='|' read -r status name secs log <<<"$row"
  line="$(printf '  %-4s  %-*s  %4s' "$status" "$width" "$name" "${secs}s")"
  if [[ "$status" == FAIL && -n "$log" ]]; then line+="  (log: $log)"; fi
  if [[ "$status" == FAIL && -z "$log" ]]; then line+="  (no log; its error is above)"; fi
  if ((secs > BUDGET)); then line+="  slow"; fi
  echo "$line"
done
((failed == 0))
