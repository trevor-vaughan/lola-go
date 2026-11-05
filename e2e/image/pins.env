# Every pin of the e2e image, sourced by the e2e runner only. The base image is pinned by the
# digest of its multi-platform index, and BASE_IMAGE is derived from it here: neither can be
# overridden from the environment. PY_LOLA_REPO and PY_LOLA_COMMIT can, to build against another
# Python lola; PY_LOLA_COMMIT must be a full 40-character SHA, which build-image.sh checks.
# shellcheck shell=bash disable=SC2034
BASE_DIGEST=sha256:ae14d4ce8771606477a81446e7ef429edd6762305fc50c84507c1041fa8285d1
BASE_IMAGE=docker.io/library/python:3.13-slim@$BASE_DIGEST
PY_LOLA_REPO="${PY_LOLA_REPO:-https://github.com/LobsterTrap/lola}"
PY_LOLA_COMMIT="${PY_LOLA_COMMIT:-91a28855fdae91c3ec07fdda44f331b10eb8c8c0}"
