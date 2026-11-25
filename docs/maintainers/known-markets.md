# The known marketplaces list

For maintainers; back to the [release checklist](../release.md). Users meet this list as
`lola market --known` and `lola market --enable <id>` ([guide](../guides/marketplaces.md#known-marketplaces)).

The list is `internal/market/known.yaml`, embedded in the binary and read by `market.Known`
(`internal/market/known.go`, which validates every field and refuses the whole file on the first
bad entry). It is the only source: there is no user file and no remote directory. Each entry is:

| Field     | Meaning |
|-----------|---------|
| `id`      | The name it is enabled as; follows the marketplace-name rule, unique. |
| `source`  | An `https://` URL without credentials. The loader refuses a catalog-file URL (path ending `.json`, `.yml` or `.yaml`); the spec on the shipped list refuses an archive URL. It must be a git repository: `task release:known` proves it is reachable. |
| `path`    | The catalog file or folder inside the repository; leave out for the root. |
| `commit`  | The full 40-hex commit, never a branch or tag. |
| `curated` | The day the pin was reviewed (`YYYY-MM-DD`). |
| `reason`  | One line of plain text: why it is listed. |

## Criteria

An entry needs all of these:

- **An owner that will stay:** an organisation, or a maintainer with a long record of the project.
- **Active:** a commit on the default branch within the last 12 months.
- **A catalog lola reads:** `lola market --enable <id> -n -y` succeeds on the pin with this build.
- **A git source:** a reachable git repository, never an archive.
- **Stars alone never qualify** an entry.

The catalog repository's license is **recorded, not required**. It governs only the catalog file,
never the plugins the catalog points to, which carry their own. Record it for the entry: a license
that is missing or proprietary is stated in the entry's `reason`, so the user sees it at the
prompt; an open license needs no mention.

Informational, not a gate: whether the plugins need hooks or install scripts to be usable.
Enabling runs nothing from a catalog either way.

## The pin

Every entry names one commit. `--enable` registers that commit and `lola market <name> --update`
fetches it again, so the catalog and the entries it keeps in the same repository never move with
a branch. An entry whose source is another repository is fetched at the ref the pinned catalog
names for it, which may be a branch head; the pin does not cover that. A newer pin reaches users
only with a newer lola.

## Add an entry

1. Check the criteria. Find the head: `git ls-remote <source> HEAD`; the last commit date with
   `gh api repos/<owner>/<repo>/commits?per_page=1`; the repository's license with
   `gh api repos/<owner>/<repo>/license` or its `LICENSE`, to state in the `reason` when it is
   missing or proprietary.
2. Add the entry to `known.yaml`, pinned to that head, with today's date as `curated`.
3. `task build`, then `lola market --enable <id> -n -y` in a throwaway `HOME` and `XDG_*`
   (or run `task release:known`). Read the entry and skipped counts it prints.
4. `go test ./internal/market/... ./internal/engine/...`: a spec loads the shipped list and
   checks each entry is sound and a git source.

## Refresh a pin

Run `task release:known` (the release checklist does). Per entry it prints the pin, the upstream
head and whether they differ, and enables the pin in a throwaway environment. A moved head is
only a note. So is a redirect: `git` follows HTTP redirects silently, so a renamed or transferred
repository still passes while the URL in `known.yaml` is stale. The script asks the source's git
endpoint for its final URL and prints `note: the source redirects to <url>`; a trailing slash or
`.git` difference is not reported, and a source that is not `http(s)` is not checked. A
repository that is gone fails instead. On a redirect, check the new location is the same project
and owner (a redirect can also be a reused name), then set the new `source`, re-pin and run the
task again. To move a pin, read what changed between the commits (the catalog file above all),
check the criteria still hold, set the new `commit` and `curated`, and run the task again.

## Drop an entry

Delete it from `known.yaml` when it fails a criterion (archived, no commit for 12 months, an owner
change) or its pinned catalog no longer enables. Users who enabled it keep their
registration; it is theirs once enabled, and `lola market <name> --remove` removes it.
