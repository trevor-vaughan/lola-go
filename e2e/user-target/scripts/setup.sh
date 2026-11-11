#!/usr/bin/env bash
# Builds the user-target suite's state inside its container: the user target spec quill (a tool
# lola has no built-in for, no binary, four component kinds, two eras: 1.x keeps commands in
# .quill/slash, 2.x in .quill/commands; the installed version is read from ~/.quill/version.json)
# and a project holding .quill/.
# The module fixture is /e2e/fixtures/pen.
set -euo pipefail
specs=/home/tester/.config/lola/targets
mkdir -p "$specs" /work/proj/.quill
cat >"$specs/quill.yaml" <<'SPEC'
id: quill
readers: "the quill editor"
probe:
  - file: {path: "{home}/.quill/version.json", pointer: /version}
roots: {project: "{project}", user: "{home}"}
detect: {project: {any: [.quill/]}, user: {any: [.quill/]}}
eras:
  - since: "1.0"
    layout:
      skill: {mech: skill-dir, path: .quill/skills}
      command: {mech: markdown-file, path: .quill/slash, ext: .md, sidecar: drop}
      agent: {mech: markdown-file, path: .quill/agents, ext: .md}
      rule: {mech: rules-dir, path: .quill/rules/lola}
  - since: "2.0"
    extends: previous
    layout:
      command: {mech: markdown-file, path: .quill/commands, ext: .md, sidecar: drop}
SPEC
echo "fixture ready"
