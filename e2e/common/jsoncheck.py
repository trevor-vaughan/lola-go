# ruff: noqa: INP001  # standalone script run by path in the e2e image, not a package
"""Check a lola --json output the way the install and template suites assert it,
then print it on one line, so the venom step that ran this asserts its fields through
result.systemoutjson.

  jsoncheck.py doc <file>                 the file is one JSON document
  jsoncheck.py refusal <stdout> <stderr>  stdout is empty and stderr is exactly one
                                          line, a JSON object with an error string and
                                          a list of string hints

Either way no value may be null but those documented as nullable: trust.targets and
its from and to, the from and to of a field in trust.changes, and the profile and
writes_to of each scope in context, and the shadowed_by of each row of lola
profile's profiles. On a failed check it prints why on stderr and exits 1.

usage: jsoncheck.py doc <file> | refusal <stdout> <stderr>"""
import json
import sys
from pathlib import Path

ARGC_DOC = 3  # jsoncheck.py doc <file>
ARGC_REFUSAL = 4  # jsoncheck.py refusal <stdout> <stderr>
LIST_ITEM_DEPTH = 3  # context/<scope>/<field>, profiles/<row>/<field>
CHANGE_FIELD_DEPTH = 6  # trust/changes/<n>/fields/<field>/<from|to>


def nullable(path):
    if path in (
        ["trust", "targets"],
        ["trust", "targets", "from"],
        ["trust", "targets", "to"],
    ):
        return True
    if (
        len(path) == LIST_ITEM_DEPTH
        and path[0] == "context"
        and path[2] in ("profile", "writes_to")
    ):
        return True
    if (
        len(path) == LIST_ITEM_DEPTH
        and path[0] == "profiles"
        and path[2] == "shadowed_by"
    ):
        return True
    return (
        len(path) == CHANGE_FIELD_DEPTH
        and path[:2] == ["trust", "changes"]
        and path[3] == "fields"
        and path[5] in ("from", "to")
    )


def fail(why):
    print(f"jsoncheck.py: {why}", file=sys.stderr)  # noqa: T201  # printed output is what the venom step asserts
    sys.exit(1)


def nulls(value, path):
    if value is None and not nullable(path):
        fail("null at /" + "/".join(path))
    if isinstance(value, dict):
        items = value.items()
    elif isinstance(value, list):
        items = enumerate(value)
    else:
        items = []
    for key, item in items:
        nulls(item, [*path, str(key)])


def read(path):
    try:
        return Path(path).read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError) as e:
        fail(f"cannot read {path}: {e}")


def parse(text):
    try:
        return json.loads(text)
    except (ValueError, RecursionError) as e:
        fail(f"not JSON ({e}): {text[:400]}")


if len(sys.argv) == ARGC_DOC and sys.argv[1] == "doc":
    doc = parse(read(sys.argv[2]))
elif len(sys.argv) == ARGC_REFUSAL and sys.argv[1] == "refusal":
    out, err = read(sys.argv[2]), read(sys.argv[3])
    if out:
        fail("stdout is not empty: " + out[:200])
    if not err.endswith("\n") or err.count("\n") != 1:
        fail("stderr is not exactly one line: " + err[:400])
    doc = parse(err)
    if not (
        isinstance(doc, dict)
        and isinstance(doc.get("error"), str)
        and isinstance(doc.get("hints"), list)
        and all(isinstance(h, str) for h in doc["hints"])
    ):
        fail("stderr is not a refusal: " + err[:400])
else:
    fail("usage: jsoncheck.py doc <file> | refusal <stdout> <stderr>")
nulls(doc, [])
print(json.dumps(doc, ensure_ascii=False))  # noqa: T201  # printed output is what the venom step asserts
