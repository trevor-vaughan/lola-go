# ruff: noqa: INP001  # standalone script run by path in the e2e image, not a package
"""Check a lola migrate run against the snapshot taken after the Python lola's
installs. Prints one "ok <check>" or "FAIL <check>: <detail>" line per check and
exits 1 when any check fails. It also prints, per assistant directory, the paths the
Python lola wrote, so the oracle can be compared with what was observed.

usage: check.py <before.json> <result.json> [--home-action move|keep]
       [--untouched DIR]...
"""
import argparse
import hashlib
import json
import os
import sys
from pathlib import Path

ap = argparse.ArgumentParser()
ap.add_argument("before")
ap.add_argument("result")
ap.add_argument("--home-action", required=True)
ap.add_argument("--untouched", action="append", default=[])
args = ap.parse_args()
before = json.loads(Path(args.before).read_text(encoding="utf-8"))
result = json.loads(Path(args.result).read_text(encoding="utf-8"))
home = os.environ["HOME"]
config = os.environ.get("XDG_CONFIG_HOME") or str(Path(home, ".config"))
state = os.environ.get("XDG_STATE_HOME") or str(Path(home, ".local", "state"))
opencode_config = str(Path(config, "opencode"))
failures = []


def report(name, problems):
    if problems:
        failures.append(name)
        print(f"FAIL {name}: " + "; ".join(sorted(problems)[:20]))  # noqa: T201  # printed output is what the venom step asserts
    else:
        print(f"ok {name}")  # noqa: T201  # printed output is what the venom step asserts


def digest(p):
    path = Path(p)
    if path.is_symlink():
        return "link:" + str(path.readlink())
    return hashlib.sha256(path.read_bytes()).hexdigest()


def under(path, root):
    return path == root or path.startswith(root.rstrip("/") + "/")


def ledger_of(dest):
    root = dest.get("root", "")
    p = Path(root, ".lola", "lock.json") if root else Path(state, "lola", "lock.json")
    return json.loads(p.read_text(encoding="utf-8")) if p.exists() else {"modules": {}}


def target_root(dest, path):
    if dest.get("root", ""):
        return dest.get("root", "")
    return opencode_config if under(path, opencode_config) else home


def owned_paths(dest):
    out = set()
    for entry in ledger_of(dest)["modules"].values():
        for a in entry["artifacts"]:
            root = dest.get("root", "") or (
                opencode_config if a["target"] == "opencode" else home
            )
            out.add((str(Path(root, a["path"])), a.get("key", "")))
    return out


replaced = [(d, item) for d in result["destinations"] for item in d["replaced"]]
manifest = []
backup_problems = []
if replaced:
    manifest_path = Path(result["trash"], "manifest.txt") if result["trash"] else None
    if manifest_path is None or not manifest_path.is_file():
        backup_problems.append(
            "the run replaced paths but left no trash with a manifest.txt",
        )
    else:
        with manifest_path.open(encoding="utf-8") as f:
            manifest = [line.rstrip("\n").split("\t") for line in f if line.strip()]
        if not manifest:
            backup_problems.append("the run replaced paths but manifest.txt is empty")
for _, item in replaced:
    if not any(under(item["path"], orig) for orig, _ in manifest):
        backup_problems.append(
            f"{item['path']} ({item['kind']}) was replaced without a backup line"
            " in manifest.txt",
        )

problems = []
for dest in result["destinations"]:
    owned = owned_paths(dest)
    for item in dest["replaced"]:
        if item["kind"] == "module-copy":
            continue
        hit = any(under(p, item["path"]) and k == item.get("key", "") for p, k in owned)
        if not hit:
            problems.append(
                f"{item['path']} {item.get('key', '')} is not in the ledger",
            )
report("the lola-go ledger owns every replaced path", problems)

problems = []
for orig, trash in manifest:
    if not os.path.lexists(trash):
        problems.append(f"{trash} is missing")
        continue
    files = {p: h for p, h in before.items() if under(p, orig)}
    if not files:
        problems.append(f"{orig} was not written by the Python lola")
    for p, h in files.items():
        copy = str(Path(trash, os.path.relpath(p, orig))) if p != orig else trash
        if not os.path.lexists(copy) or digest(copy) != h:
            problems.append(f"{copy} does not match the snapshot of {p}")
report(
    "every manifest.txt line names a trash path matching the snapshot",
    problems + backup_problems,
)

problems = []
for dest in result["destinations"]:
    if dest["status"] not in ("migrated", "unchanged"):
        where = dest.get("root") or "user"
        problems.append(f"{where} is {dest['status']}: {dest.get('error', '')}")
report("every destination migrated", problems)

problems = []
listed = {
    (i["what"], i["path"]) for d in result["destinations"] for i in d["not_migrated"]
}
for d in result["destinations"]:
    for k in d["kept"]:
        listed.add(("kept", k["path"]))
for what, path in listed:
    if (
        what in ("instructions", "gemini-skills", "lola-req")
        and not Path(path).exists()
    ):
        problems.append(f"{path} is reported but gone")
report("kept and not-migrated items are reported and still in place", problems)

problems = []
for dest in result["destinations"]:
    if not dest.get("root", ""):
        continue
    lola = Path(dest.get("root", ""), ".lola")
    names = set(ledger_of(dest)["modules"]) | {u["module"] for u in dest["unmappable"]}
    for entry in lola.iterdir():
        if entry.name not in (
            ".gitignore",
            "lock.json",
            "project.yml",
            "modules",
            "plugin-data",
        ):
            problems.append(f"{lola}/{entry.name}")
    if (lola / "modules").is_dir():
        for m in (lola / "modules").iterdir():
            if m.name not in names:
                problems.append(
                    f"{lola}/modules/{m.name} is neither a lola-go module"
                    " nor kept for an unmappable record",
                )
report("what remains in .lola/ is lola-go's or kept for an unmappable record", problems)

problems = []
for root in args.untouched:
    for p, h in before.items():
        if under(p, root) and (not os.path.lexists(p) or digest(p) != h):
            problems.append(p)
if args.untouched:
    report(
        "the unmappable records' files are byte-identical: "
        + ", ".join(args.untouched),
        problems,
    )

# The oracle: every file the Python lola wrote under an assistant directory lola-go
# migrates is now lola-go's, in the trash, reported as kept or not migrated, or left
# untouched for an unmappable record.
problems = []
accounted = set(args.untouched)
for d in result["destinations"]:
    accounted |= (
        {p for p, _ in owned_paths(d)}
        | {k["path"] for k in d["kept"]}
        | {i["path"] for i in d["not_migrated"]}
    )
written = {}
for p in before:
    for marker in (
        "/.claude/",
        "/.gemini/commands/",
        "/.opencode/",
        "/.lola/modules/",
        "/.config/opencode/",
    ):
        if (
            marker in p
            and "/.lola/modules/" not in p.split(marker)[0]
            and not p.startswith(str(Path(home, ".lola")) + "/")
        ):
            written.setdefault(marker.strip("/"), []).append(p)
            if not (
                any(under(p, a) for a in accounted)
                or any(under(p, orig) for orig, _ in manifest)
            ):
                problems.append(p)
for marker, paths in sorted(written.items()):
    print(f"observed {marker}: " + ", ".join(sorted(paths)))  # noqa: T201  # printed output is what the venom step asserts
report(
    "every path the Python lola wrote is lola-go's, in the trash, kept, reported"
    " or untouched",
    problems,
)

problems = []
action = result["home"]["action"]
if action != args.home_action:
    problems.append(
        f"home action is {action}: {result['home'].get('reason', '')}",
    )
if action == "move" and (
    os.path.lexists(result["home"]["path"])
    or not Path(result["home"]["trash"]).is_dir()
):
    problems.append("the home was not moved into the trash")
report(f"the Python home action is {args.home_action}", problems)

sys.exit(1 if failures else 0)
