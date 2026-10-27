"""Check a lola --json output the way the install and template suites assert it (Plan 8 §4.0),
then print it on one line, so the venom step that ran this asserts its fields through
result.systemoutjson.

  jsoncheck.py doc <file>                 the file is one JSON document
  jsoncheck.py refusal <stdout> <stderr>  stdout is empty and stderr is exactly one line, a JSON
                                          object with an error string and a list of string hints

Either way no value may be null but those documented as nullable: trust.targets and its from
and to, and the from and to of a field in trust.changes. On a failed check it prints why on
stderr and exits 1.

usage: jsoncheck.py doc <file> | refusal <stdout> <stderr>"""
import json
import sys


def nullable(path):
    if path in (["trust", "targets"], ["trust", "targets", "from"], ["trust", "targets", "to"]):
        return True
    return (len(path) == 6 and path[:2] == ["trust", "changes"] and path[3] == "fields"
            and path[5] in ("from", "to"))


def fail(why):
    print(f"jsoncheck.py: {why}", file=sys.stderr)
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
        nulls(item, path + [str(key)])


def read(path):
    try:
        with open(path, encoding="utf-8") as f:
            return f.read()
    except (OSError, UnicodeDecodeError) as e:
        fail(f"cannot read {path}: {e}")


def parse(text):
    try:
        return json.loads(text)
    except (ValueError, RecursionError) as e:
        fail(f"not JSON ({e}): {text[:400]}")


if len(sys.argv) == 3 and sys.argv[1] == "doc":
    doc = parse(read(sys.argv[2]))
elif len(sys.argv) == 4 and sys.argv[1] == "refusal":
    out, err = read(sys.argv[2]), read(sys.argv[3])
    if out:
        fail("stdout is not empty: " + out[:200])
    if not err.endswith("\n") or err.count("\n") != 1:
        fail("stderr is not exactly one line: " + err[:400])
    doc = parse(err)
    if not (isinstance(doc, dict) and isinstance(doc.get("error"), str)
            and isinstance(doc.get("hints"), list)
            and all(isinstance(h, str) for h in doc["hints"])):
        fail("stderr is not a refusal: " + err[:400])
else:
    fail("usage: jsoncheck.py doc <file> | refusal <stdout> <stderr>")
nulls(doc, [])
print(json.dumps(doc, ensure_ascii=False))
