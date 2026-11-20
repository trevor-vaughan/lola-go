# ruff: noqa: INP001  # standalone script run by path in the e2e image, not a package
"""Run one hint of a refusal's --json line, as a user pasting it would: the first,
or the one at the index given (a trust refusal's second hint applies what its
first shows).

Hints carry author text, so this runs only lola: it takes the chosen hint alone,
accepts it only as "lola ..." or "<label>: lola ..." with a lowercase label (the
house form, "see: lola ..."), splits it with shlex, requires the first word to be
lola, puts the binary under test in its place and runs it without a shell, with a
timeout. The hint's own output and exit status are this script's; a hint it
refuses or cannot run, or input it cannot read, prints why on stderr and exits 2.

usage: hint.py <file holding the refusal's JSON line> [<index>]"""
import json
import re
import shlex
import subprocess
import sys
from pathlib import Path

LOLA = "/opt/lola-go/lola-go"
ARGC_FILE = 2  # hint.py <file>
ARGC_INDEX = 3  # hint.py <file> <index>
LABEL = re.compile(r"[a-z][a-z ,]*")


def refuse(why):
    print(f"hint.py: {why}", file=sys.stderr)  # noqa: T201  # printed output is what the venom step asserts
    sys.exit(2)


def command(hint):
    if hint.startswith("lola "):
        return hint
    label, sep, rest = hint.partition(": ")
    if sep and LABEL.fullmatch(label) and rest.startswith("lola "):
        return rest
    return None


if len(sys.argv) not in (ARGC_FILE, ARGC_INDEX) or (
    len(sys.argv) == ARGC_INDEX and not re.fullmatch(r"[0-9]+", sys.argv[2])
):
    refuse("usage: hint.py <file holding the refusal's JSON line> [<index>]")
index = int(sys.argv[2]) if len(sys.argv) == ARGC_INDEX else 0
try:
    doc = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
except (OSError, ValueError, RecursionError) as e:
    refuse(f"cannot read a refusal from {sys.argv[1]!r}: {e}")
if not isinstance(doc, dict):
    refuse("the refusal is not a JSON object")
hints = doc.get("hints", [])
if not isinstance(hints, list) or not all(isinstance(h, str) for h in hints):
    refuse("the refusal's hints are not a list of strings")
if not hints:
    refuse("the refusal has no hint")
if index >= len(hints):
    refuse(f"the refusal has no hint {index}")
hint = hints[index]
lola_command = command(hint)
if lola_command is None:
    refuse(f"not a lola command: {hint!r}")
try:
    argv = shlex.split(lola_command)
except ValueError as e:
    refuse(f"cannot split {hint!r}: {e}")
if argv[0] != "lola":
    refuse(f"not a lola command: {hint!r}")
argv[0] = LOLA
try:
    sys.exit(subprocess.run(argv, shell=False, timeout=60, check=False).returncode)
except subprocess.TimeoutExpired:
    refuse(f"{hint!r} ran past 60 s")
except OSError as e:
    refuse(f"cannot run {LOLA}: {e}")
except ValueError as e:
    refuse(f"cannot run {hint!r}: {e}")
