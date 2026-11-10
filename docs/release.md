# Releasing lola

The checklist a maintainer works through before tagging a release. Every item has a pass
condition; any item that fails blocks the tag until it passes. Run the items on the commit you
mean to tag, with a clean work tree.

1. **The gates.** `task check` is green (lint, test, cross, vuln, flows; cross builds and vets for Windows
   and macOS).

2. **The end-to-end suites.** `task e2e MODE=llm` is green on Linux with podman or docker, and
   the summary marks no suite `slow` (each within 90 s with the image cached). `MODE=llm` prints
   one line per passing suite and the failures of a failing one; the summary it ends with has a
   line per suite and marks `slow` any suite over the 90 s budget. Keep each suite's
   `.test-output/e2e-<suite>/versions.txt` with the release's records: it says which runtime ran,
   whether rootless, and venom's version ([testing.md](testing.md#end-to-end-suites)). Docker
   is unverified ([testing.md](testing.md#untested-by-design)).

3. **The vendored liquid's advisories** (checked by hand before each release). `third_party/liquid`
   is `github.com/osteele/liquid` v1.9.2 with one patch, under a `replace` in `go.mod`.
   `govulncheck` reports the module as `./third_party/liquid` with no version, so it cannot match
   advisories for `github.com/osteele/liquid` to it, and `task vuln` passing says nothing about
   them. Read every advisory for `github.com/osteele/liquid` published since v1.9.2 in:
   - the GitHub advisory database (GHSA),
     <https://github.com/advisories?query=osteele%2Fliquid>;
   - the Go vulnerability database, <https://pkg.go.dev/vuln/> (vuln.go.dev), searching for
     `github.com/osteele/liquid`;
   - the project's repository security advisories,
     <https://github.com/osteele/liquid/security/advisories>.

   Judge each against the vendored code and `third_party/liquid/PATCHES.md`. An advisory that
   applies to the vendored code and is not patched there blocks the tag. Record the date, every
   advisory ID read and each verdict (applies and patched, applies and blocks, or does not apply,
   with why) under "Vulnerability checks" in `PATCHES.md`, in the release commit. That commit
   changes only the record, so the other items are not run again on it.

4. **The Go toolchain.** `go.mod`'s `toolchain` line names the newest patch release of the pinned
   minor (`go1.26.x`), and `task vuln` is clean on it.

5. **The e2e image's base.** `BASE_DIGEST` in `e2e/image/pins.env` is the digest of the
   multi-platform index the e2e image's base, `python:3.13-slim`, is pinned by. It still resolves
   (the runner pulls `docker.io/library/python:3.13-slim@<digest>`), and the Debian security tracker
   (<https://security-tracker.debian.org/tracker/>) shows no fixed security update for the
   image's Debian release newer than the digest. Otherwise bump `BASE_DIGEST` and run item 2
   again.

6. **The docs, by hand.** Compare:
   - the commands `lola help` lists with the command pages of [reference/](reference/): piped,
     `lola help` prints one `== lola … ==` heading per command and subcommand (for example
     `== lola cache add ==`). Every heading but `== lola ==` has a `` # `lola …` `` or
     `` ## `lola …` `` heading there (`lola cache add` is a section of `cache.md`), and every such
     heading names a command `lola help` lists;
   - README's "What works today" with what ships: nothing listed that does not work;
   - README's quickstart with the cases it is written from. Without the container wrapper and the
     suites' own paths and flags, the commands in the quickstart's steps after the `PATH` step are
     the commands of `Q1` in `e2e/install/install.venom.yml`, and the commands in its teammate
     steps are those of `T1` and `T4` in `e2e/team/team.venom.yml`.

7. **Release.** On the commit every item above passed on, plus item 3's record committed as
   `chore(release): …` so it stays out of the notes, cut the release:

   ```sh
   task release:notes             # review the notes the release will carry
   task release:bump              # prompts for the version git-cliff suggests
   task release:bump -- 0.1.0     # or name it
   ```

   - **What it does:** regenerates `CHANGELOG.md`, commits it as `chore(release): vX.Y.Z`, and
     puts an annotated tag `vX.Y.Z` on that commit. [git-cliff](https://git-cliff.org) generates
     the changelog from the Conventional Commits since the last `vX.Y.Z` tag (`cliff.toml`);
     commits from before the convention are left out. Never edit `CHANGELOG.md` by hand.
   - **The version:** the prompt's default is git-cliff's suggestion. While the major version is
     0, a breaking change bumps the minor and anything else the patch; 1.0.0 is never suggested.
     The first release is suggested as 0.1.0.
   - **The tag's message:** `lola vX.Y.Z`, then the release's section of the changelog.
   - **Refusals:** a version that is not `X.Y.Z` or not after the last release, a tag that
     exists, a dirty work tree, and a release with no Conventional Commit to list. If writing or committing `CHANGELOG.md` fails, it is restored; if only the tag
     fails, the error says how to drop the release commit and run again.
   - **git-cliff:** both tasks first install the pinned git-cliff 2.14.2 into `.tools/bin`
     (`task release:tools`, which checks the download's SHA-512). On a platform with no pinned
     download, install it there yourself:
     `cargo install --locked git-cliff --version 2.14.2 --root .tools`.

   Nothing on this list publishes anything. `release:bump` never pushes
   (`git push origin HEAD vX.Y.Z` does, when you are ready), no image is pushed and no binary
   uploaded; there is no release automation.
