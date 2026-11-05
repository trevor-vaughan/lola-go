"""Record every file and symbolic link below the given roots as JSON: path -> sha256 or link
target. The tree after the Python lola's installs is the oracle the checks compare against.
lola's cache is left out: a dry run fetches into it, as lola install --dry-run does.

With --compare, read two such snapshots instead and print "unchanged" (exit 0) when they record
the same paths with the same contents, but for paths that start with an ignored prefix; else
print the changed paths, sorted, one per line (exit 1).

usage: snapshot.py <out.json> <root>...
       snapshot.py --compare <before.json> <after.json> [<ignored prefix>...]"""
import hashlib
import json
import os
import sys

if sys.argv[1] == "--compare":
    with open(sys.argv[2]) as f:
        before = json.load(f)
    with open(sys.argv[3]) as f:
        after = json.load(f)
    ignored = tuple(sys.argv[4:])
    changed = sorted(p for p in before.keys() | after.keys()
                     if before.get(p) != after.get(p) and not (ignored and p.startswith(ignored)))
    print("\n".join(changed) if changed else "unchanged")
    sys.exit(1 if changed else 0)

out = {}
for root in sys.argv[2:]:
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if os.path.join(dirpath, d) != os.path.join(os.environ["HOME"], ".cache")]
        for name in filenames + [d for d in dirnames if os.path.islink(os.path.join(dirpath, d))]:
            p = os.path.join(dirpath, name)
            if os.path.islink(p):
                out[p] = "link:" + os.readlink(p)
            else:
                with open(p, "rb") as f:
                    out[p] = hashlib.sha256(f.read()).hexdigest()
with open(sys.argv[1], "w") as f:
    json.dump(out, f, indent=1, sort_keys=True)
print(f"snapshot: {len(out)} files")
