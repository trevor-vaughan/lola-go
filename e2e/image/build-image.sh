#!/usr/bin/env bash
# Runs inside the base image; the result is committed as the e2e suites' image. It installs the
# Python lola from upstream at the full-SHA commit the migrate design was checked against (it
# refuses to build unless the checkout is exactly that commit), with every Python package it
# needs (and its build backend) pinned by hash in /e2e-image/requirements.txt, so pip fetches
# nothing unpinned: the dependencies first, then the Python lola alone. The image
# is built with run and commit, not a Containerfile, because buildah cannot mount a build
# context where overlay mounts nest (rootless podman inside a container); run and commit work
# with podman and docker everywhere.
set -euo pipefail
: "${PY_LOLA_REPO:?}" "${PY_LOLA_COMMIT:?}"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends git zip >/dev/null
rm -rf /var/lib/apt/lists/*
git clone --quiet "$PY_LOLA_REPO" /opt/py-lola
git -C /opt/py-lola checkout --quiet "$PY_LOLA_COMMIT"
resolved="$(git -C /opt/py-lola rev-parse HEAD)"
if [[ "$resolved" != "$PY_LOLA_COMMIT" ]]; then
  echo "error: PY_LOLA_COMMIT=$PY_LOLA_COMMIT resolved to $resolved; give the full 40-character SHA" >&2
  exit 1
fi
pip install --quiet --no-cache-dir --root-user-action=ignore --require-hashes --no-deps -r /e2e-image/requirements.txt
pip install --quiet --no-cache-dir --root-user-action=ignore --no-deps --no-build-isolation /opt/py-lola
pip check
mkdir -p /home/tester /work
lola --version
