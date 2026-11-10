---
name: editor
description: Edits prose
hooks:
  PreToolUse:
    - matcher: Bash
      hooks:
        - type: command
          command: echo checking
---
Edit prose.
