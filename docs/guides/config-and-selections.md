# `config.yaml` and `selections.yaml`

What lola keeps in `config.yaml` and `selections.yaml`. It is split out of [choosing-what-to-install](choosing-what-to-install.md).

Both live in `$XDG_CONFIG_HOME/lola/` (normally `~/.config/lola/`), are safe to edit by hand,
and are checked strictly: unknown keys, wrong types, unknown kinds, bad globs, relative or
`~` project keys, a `git:` key without `#<dir>`, two keys for one project, control or format characters in selector names,
an `include` in the `global` section, anchors, aliases and merge keys, and files over
1 MiB are errors that name the file, line and key, with a hint.

```yaml
# lola settings. Safe to edit by hand.
version: 1
default_scope: project   # project | user | both; read off a terminal only
pager: true              # false prints everything without lola's viewer
pre_install: ~/bin/lola-pre.sh     # your own install scripts: absolute or ~/…, no trust asked
post_install: ~/bin/lola-post.sh
cache_unused_for: 30d    # lola cache clean also removes versions unused this long (<n>d, <n>w, never)
cache_fresh_for: 1d      # lola update serves a ref fetched less long ago from your cache (<n>d, <n>w, never)
theme: dark              # your terminal's background, dark or light, for the pickers' colors
```

`selections.yaml` says which components you do not want of the modules `project.yml` lists, and
globally. A module a profile provides, `default.yml` included, takes its picks from its profile
entry's `only:` and `skip:` instead (plus the global `skip:`): a profile carries what it installs,
picks included, wherever it is switched on. At user scope every module comes from a profile, so
`selections.yaml`'s global `only:` no longer applies there; the picks live in the entries
(`lola install <module> --user --skip skill/slow-lint`). `install`, `sync`, `update` and `check`
all honor the file, so editing it takes effect on the next `sync` (a component you skip after
installing it is removed, unless you edited its file, which is kept and reported):

```yaml
# lola selections: components you chose not to install.
version: 1
global:
  review-kit:                       # module name as `lola list` shows it
    skip: [skill/slow-lint, mcp/internal-db]
projects:
  git:github.com/acme/api#.:          # a clone of github.com/acme/api, at its top
    review-kit:
      skip: [command/deploy]
      include: [skill/slow-lint]
    cc-skills-golang:
      only: ['skill/golang-test*', skill/golang-gopls]   # install these, and nothing a later version adds
```

A file with a `user:` section is refused (`user: is no longer used; picks for user-scope modules
live in their profile entries`): delete the section, then re-apply each pick with `lola install
<module> --user --skip <kind/name>`.

A module's selection at a scope combines, in this order: the scope's `include`, which always
installs a match; the skips of `global` and of the scope; and one `only:` list — the scope's if
it has one, else `global`'s — which replaces the other (no intersection) and leaves out every
component it does not match. `only: []` is no list. `global` has no includes. A selector naming
a component the module no longer has is a warning that names its section. With `--global`,
`--only` or `--unskip` cannot override a glob skip in `global` (it takes no include), so when one
still skips a component you listed, lola warns, naming the glob and `selections.yaml`: remove
the glob there.

Project keys name the project by its repository when it is a git clone whose `origin` remote is on
another host: `git:<host>/<path>#<dir>`, the remote without its scheme, user name, password or
token, query, default port (a port other than the scheme's default stays: `git:<host>:<port>/<path>#<dir>`)
and `.git`, its host in lower case, and the project's directory in the clone (`.` at
its top, `sub/dir` for a project below it).

Every clone of the repository, wherever you put it,
uses one entry, so a re-clone keeps your picks, and two clones on one machine share them. Any
other project, and a clone with no `origin` or a local one, or whose remote would leave an `@` in
the key, is keyed by its absolute path; a key written through a symlink or with `..` still matches
its project.

In a clone git cannot read (one git refuses as someone else's, say), `install`, `sync`
and `lola list` refuse before they read any pick, as they cannot tell whether git tracks
`.lola/lock.json`; `lola check` and `lola info` key its picks by its path and say why, with git's
reason (`lola check` as the `picks-keyed-by-path` finding), and for a repository git refuses as
someone else's the `git config --global --add safe.directory <dir>` that trusts it.

An entry under a clone's path,
written before lola keyed clones by repository, still applies, and the next pick lola saves for
one of its modules moves it under the repository key, leaving the file's other entries as they
are.

These picks are yours: `selections.yaml` lives in your config directory, so a teammate who
clones the project does not get them. A [project profile](profiles.md) is how a team shares
picks: its entries' `only:` and `skip:` are committed with the project, and `lola profile --move
<module> --to project:<name>` moves a module, picks included, into one.

An install
whose `--only`, `--skip` or `--unskip` saves picks in a project's section of `selections.yaml` says
so: `picks for review-kit saved in selections.yaml
projects[git:github.com/acme/api#.]: they are yours, not your teammates'; a project profile shares
picks with your team`.

lola writes the file under a lock, replacing only the entries of the
modules it changes, so your comments, order and other edits survive, even in a file that holds
only comments or a null document. A section or project key that a removal empties is dropped,
unless it carries a comment, in which case it stays as an empty mapping. A symlinked file is
written at its target. A file that does not parse is never overwritten. Entries for projects you
moved are left alone.
