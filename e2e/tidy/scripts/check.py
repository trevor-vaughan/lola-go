"""Checks lola tidy's moves against the suite's expectations.

  check.py snapshot <out.json> <path>...         records each path's tree, by its real path: for a directory every
                                                 entry below it, each file's content and each
                                                 link's target; for a file its content
  check.py moved <result.json> <snap.json> <kind/name>...
                                                 the run moved exactly these units: each original
                                                 path is gone (an MCP server: its file stays,
                                                 without it), each trash path exists on another
                                                 file system than its original's parent and holds
                                                 the snapshot's tree and contents, and the run's
                                                 manifest.txt names each trash path once
  check.py restore <result.json> <snap.json>     moves every item of the run's manifest.txt back
                                                 with mv, then checks each restored path holds
                                                 the snapshot's tree and contents

Prints one "ok ..." line per check that passed and "FAIL ..." for each that did not; exits 1 on
any failure.
"""
import json
import os
import subprocess
import sys

failures = 0


def check(ok, what):
    global failures
    print(("ok " if ok else "FAIL ") + what)
    if not ok:
        failures += 1


def tree(path):
    """The tree at path: {relative path: "file:<content>" | "link:<target>" | "dir"}, "." itself."""
    def entry(p):
        if os.path.islink(p):
            return "link:" + os.readlink(p)
        if os.path.isdir(p):
            return "dir"
        with open(p, encoding="utf-8") as f:
            return "file:" + f.read()

    out = {".": entry(path)}
    if out["."] == "dir":
        for dirpath, dirnames, filenames in os.walk(path):
            for name in dirnames + filenames:
                p = os.path.join(dirpath, name)
                out[os.path.relpath(p, path)] = entry(p)
    return out


def load(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def manifest(result):
    with open(os.path.join(result["trash"], "manifest.txt"), encoding="utf-8") as f:
        return [line.rstrip("\n").split("\t") for line in f if line.strip()]


def moved(result, snap, want):
    got = sorted(u["kind"] + "/" + u["name"] for u in result["moved"])
    check(got == sorted(want), "moved exactly " + ", ".join(sorted(want)) + " (got " + ", ".join(got) + ")")
    for u in result["moved"]:
        name = u["kind"] + "/" + u["name"]
        if u.get("key"):
            servers = load(u["path"]).get("mcpServers", {})
            check(u["name"] not in servers, name + " left " + u["path"] + ", which lola tidy kept")
        else:
            check(not os.path.lexists(u["path"]), name + " left " + u["path"])
        check(os.path.lexists(u["trash"]), name + " is in the trash at " + u["trash"])
        parent = os.stat(os.path.dirname(u["path"])).st_dev
        check(os.lstat(u["trash"]).st_dev != parent, name + " crossed file systems into the trash")
        check(snap.get(os.path.realpath(u["path"])) == tree(u["trash"]),
              name + "'s trash copy holds the original's tree and contents")
    lines = manifest(result)
    check(sorted(t for _, t in lines) == sorted({u["trash"] for u in result["moved"]}), "manifest.txt names every trash path once")


def restore(result, snap):
    lines = manifest(result)
    for orig, trash in lines:
        subprocess.run(["mv", trash, orig], check=True)
    for orig, _ in lines:
        check(snap.get(os.path.realpath(orig)) == tree(orig), "restored " + orig + " with its tree and contents")
    check(len(lines) > 0, "manifest.txt lists what the run moved")


def main():
    cmd, args = sys.argv[1], sys.argv[2:]
    if cmd == "snapshot":
        with open(args[0], "w", encoding="utf-8") as f:
            json.dump({os.path.realpath(p): tree(p) for p in args[1:]}, f)
        print("snapshot of %d paths" % len(args[1:]))
    elif cmd == "moved":
        moved(load(args[0]), load(args[1]), args[2:])
    elif cmd == "restore":
        restore(load(args[0]), load(args[1]))
    else:
        sys.exit("unknown check " + cmd)
    sys.exit(1 if failures else 0)


main()
