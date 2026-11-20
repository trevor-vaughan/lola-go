# ruff: noqa: INP001  # standalone script run by path in the e2e image, not a package
"""Record every file and symbolic link below the given roots as JSON: path -> sha256
or link target. The tree after the Python lola's installs is the oracle the checks
compare against. lola's cache is left out: a dry run fetches into it, as lola
install --dry-run does.

With --compare, read two such snapshots instead and print "unchanged" (exit 0) when
they record the same paths with the same contents, but for paths that start with an
ignored prefix; else print the changed paths, sorted, one per line (exit 1).

usage: snapshot.py <out.json> <root>...
       snapshot.py --compare <before.json> <after.json> [<ignored prefix>...]"""
import hashlib
import json
import os
import sys
from pathlib import Path

if sys.argv[1] == "--compare":
    before = json.loads(Path(sys.argv[2]).read_text(encoding="utf-8"))
    after = json.loads(Path(sys.argv[3]).read_text(encoding="utf-8"))
    ignored = tuple(sys.argv[4:])
    changed = sorted(
        p
        for p in before.keys() | after.keys()
        if before.get(p) != after.get(p) and not (ignored and p.startswith(ignored))
    )
    print("\n".join(changed) if changed else "unchanged")  # noqa: T201  # printed output is what the venom step asserts
    sys.exit(1 if changed else 0)

out = {}
cache = Path(os.environ["HOME"]) / ".cache"
for root in sys.argv[2:]:
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if Path(dirpath, d) != cache]
        for name in filenames + [d for d in dirnames if Path(dirpath, d).is_symlink()]:
            p = Path(dirpath, name)
            if p.is_symlink():
                out[str(p)] = "link:" + str(p.readlink())
            else:
                out[str(p)] = hashlib.sha256(p.read_bytes()).hexdigest()
with Path(sys.argv[1]).open("w", encoding="utf-8") as f:
    json.dump(out, f, indent=1, sort_keys=True)
print(f"snapshot: {len(out)} files")  # noqa: T201  # printed output is what the venom step asserts
