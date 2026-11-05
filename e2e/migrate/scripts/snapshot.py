"""Record every file and symbolic link below the given roots as JSON: path -> sha256 or link
target. The tree after the Python lola's installs is the oracle the checks compare against.
lola's cache is left out: a dry run fetches into it, as lola install --dry-run does.

usage: snapshot.py <out.json> <root>..."""
import hashlib
import json
import os
import sys

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
