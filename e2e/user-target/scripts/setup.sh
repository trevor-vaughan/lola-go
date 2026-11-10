#!/usr/bin/env bash
# Builds the user-target suite's state inside its container: the user target spec quill (a tool
# lola has no built-in for, no binary, four component kinds) and a project holding .quill/.
# The module fixture is /e2e/fixtures/pen.
set -euo pipefail
specs=/home/tester/.config/lola/targets
mkdir -p "$specs" /work/proj/.quill
cat >"$specs/quill.yaml" <<'SPEC'
id: quill
readers: "the quill editor"
roots: {project: "{project}", user: "{home}"}
detect: {project: {any: [.quill/]}, user: {any: [.quill/]}}
eras:
  - since: "1.0"
    layout:
      skill: {mech: skill-dir, path: .quill/skills}
      command: {mech: markdown-file, path: .quill/commands, ext: .md, sidecar: drop}
      agent: {mech: markdown-file, path: .quill/agents, ext: .md}
      rule: {mech: rules-dir, path: .quill/rules/lola}
SPEC
echo "fixture ready"
