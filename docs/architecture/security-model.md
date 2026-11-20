# Security model

Back to the [README](../../README.md).

Maintainer notes on Security model. It is split out of [architecture](../architecture.md).

The table lists each threat, the path it takes to lola, the mitigation and the section below that
covers it. The sections give the detail, including each residual risk.

| Threat | Path | Mitigation | Section |
|---|---|---|---|
| A flag or consent grants more than the user meant | `--trust`, `-y`, `--force`, a hint | Each kind answers a fixed set of questions; hints add only the kinds missing; grants are recorded after the receipt's yes | **Trust kinds cannot widen** |
| Module code runs without informed consent | Install hooks, runtime hooks, an agent's hooks | Consent tied to the exact scripts, or to the rendered groups and module version; only content shown in full is granted | **Hooks run code, with consent** |
| A module starts an unwanted program | MCP servers written into the assistant's files | `entries` and `runtime-hooks` consent; the trust diff prints each command; lola never starts a server | **Module MCP servers** |
| Module content reads or writes outside its tree | Symlinks, archive links, odd names | Relative in-tree links only, read through `os.Root`; archives never write a link; names validated | **Sources are not trusted** |
| A checkout redirects lola's writes | Symlinked directories in the project | Conflict detection; the plan shows every redirected destination | **Destinations** |
| A repository points lola's own directories elsewhere | A symlinked `.lola/`, `.lola/modules/` or `.lola/plugin-data/` | Such links are refused; a module copy that is not a real directory is replaced | **Project state** |
| A fetch returns other content than expected | The network, git config, redirects | Digests and commits are checked on refetch; checkouts run no filter program | **Fetched sources** |
| A remote profile reaches inside your network | A host name that resolves to an internal address | `https://`/`ssh://` hosts by name only; a planted `lock.json` changes only an annotation | **Remote profiles** |
| A catalog runs code or escapes its tree | A stranger's marketplace JSON | Parsed by a package with no file access; sources classified and checked; no field runs a command | **Marketplace catalogs** |
| Recorded sources or text carry an attack | Someone else's `project.yml` or `lock.json` | Checked on load, before any git run; printed text passes `untrusted.Text` | **Recorded sources** |
| A pulled manifest installs content you did not review | `git pull`, a hand edit of `project.yml` | The trust diff; only a yes or `--trust` acts, never `-y`; untrusted content stays in staging | **Pulled manifests** |
| Tidy destroys or redirects files | Links onto the root or another project | It never deletes, only moves to the trash, and only declared entries | **Tidy** |
| A template reaches the machine | Liquid in module files | A sandbox with no file, environment, network or Go method; output capped and checked | **Templates are untrusted input** |
| A synced `pins.yaml` upgrades or fetches without asking | A dotfile manager | Strict validation; fetched only from allowed sources; commit and digest must match | **`pins.yaml` is synced input** |
| A malformed file is overwritten | Hand edits, a partial sync | Unparseable files are never rewritten | **Unparseable files** |
| A config file feeds the terminal or the parser | `config.yaml`, `selections.yaml` | Size caps, strict validation, `untrusted.Text` | **User files are untrusted** |
| Another local user reads lola's state | File modes | State files are `0600`; XDG directories `0700` | **lola's own state files** (the last bullet) |

- **Trust kinds cannot widen.** Each kind answers a fixed set of questions.
  - Bare `--trust` covers only `entries,profiles`. lola runs no code for them, but accepting
    entries is also the consent to the MCP servers they list (see **Module MCP servers**).
  - A kind a command never asks is refused.
  - A hint adds exactly the kinds its refusal misses and never writes `all`.
  - `-y` and `--force` grant none.
  - A grant is recorded only after the receipt's yes, so a No, a refusal or `-n` leaves no
    consent.
  - **Residual risk:** a grant by `--trust` is recorded as a yes is, so on a machine whose
    `$XDG_STATE_HOME` persists (a CI runner), one job's `--trust=hooks` covers later jobs
    installing the same hook bytes; the digest keys bound it, and a fresh `XDG_STATE_HOME` per job
    removes it.
- **Hooks run code, with consent.**
  - Install hooks run as the user, unconfined (not sandboxed), only after consent
    tied to the exact scripts.
  - A changed script asks again; a changed file that a script runs does not, which the user docs
    say.
  - Runtime hooks reach Claude Code's settings only under a consent tied to the rendered groups
    and the module version, so everything they could run from the module copy is covered.
  - They go to the per-user settings file, never the shared one, and only when git cannot commit
    it.
  - `--trust`'s consent kinds grant only content lola has printed in full to this user before (the
    shown record, [Trust kinds](trust-kinds.md#the-shown-record)), so a pull that changes a hook
    through an entry's vars or targets, or a project script, cannot ride on a flag given for
    something else; `--unseen` lifts that, by name, for CI.
- **Module MCP servers.**
  - lola writes them as members of the assistant's own files: `.mcp.json`, `~/.claude.json`,
    `.gemini/settings.json` and `opencode.json`.
  - `entries` consent covers a module's servers; the trust diff prints each server's command
    before you accept. `runtime-hooks` consent covers an agent's servers, from its frontmatter
    `mcpServers`.
  - lola never starts a server. Your assistant does.
  - **Residual risk:** at user scope some assistants start a server without asking again, so
    accepting the entry is the only prompt you get.
- **Sources are not trusted.**
  - A symlink in module content is copied as the regular file it names only when all of these hold:
    - it is relative;
    - it resolves, through at most 8 links and no linked directory, to a regular file;
    - that file is inside the source tree (the folder given, the clone, the extracted archive);
    - lola would copy that file (not `.git`, `node_modules` and the other ignored names, nor a
      file git ignores in a folder's work tree).
  - `resolveLink` reads every hop through an `os.Root` of the source tree, and the copy opens the
    target through it and checks it is the file resolved.
  - Archives never write a link: link entries resolve lexically against the extracted files, and
    their copies count against the byte limit.
  - Every other link is refused (`ErrLinkInSource`), as is a file swapped for a symlink while it is
    copied, and the cache never holds a link.
  - Names that become paths or JSON keys are validated, and `CopyTree` reads through `os.Root`.
    Cache names and version ids are validated before use.
- **Destinations.** lola follows symlinked directories in the project, because dotfile managers
  use them, and shows writes that leave the project. **Residual risk:** a repository you check
  out can contain symlinks that redirect lola's writes elsewhere in your home directory.
  Conflict detection prevents overwriting content lola did not write, and the plan shows every
  redirected destination; review `lola install --dry-run` in repositories you do not trust.
- **Project state.** lola refuses a `.lola/`, `.lola/modules/` or `.lola/plugin-data/` that is
  a symlink, so a repository cannot point the module-copy swap, uninstall cleanup or plugin
  data outside the project, and it replaces a module copy that is not a real directory, so a
  committed link cannot point `${PLUGIN_ROOT}` and install hooks elsewhere.
- **Fetched sources.** Residual risks: lola honors the user's environment and git config
  (credential helpers, `insteadOf`, proxies) by design; git follows an initial http redirect
  itself, possibly to plain http; and a server can serve different content for the same ref.
  `cache update` and content digests only make a change visible; a version
  fetched again for an install must match its recorded digest, and a git commit must be on its
  ref. Checkouts convert nothing and run no filter program a repository's attributes name.
- **Remote profiles.** A remote profile and its entries reach `https://`/`ssh://` hosts by name
  only; lola never resolves names, so a name that resolves inside your network, and
  a redirect to an address inside it that git or the archive fetch follows, are still fetched
  (residual). A planted, untracked `lock.json` can change only the 'installed' annotation of
  `--add`/`--update`, never what is shown.
- **Marketplace catalogs.** A catalog is a stranger's input.
  - It is parsed by a package that cannot import `os` or `io/fs` (depguard), so no catalog string
    is looked up on disk.
  - Every source it names is classified from its text, must be `https://` at a host by
    name (`source.CheckRemote`; ssh is skipped unless you allowed its host, as it would use your
    keys on a stranger's host; relaxed per host by the 2026-10-28 user decision), and
    passes the recorded-source rules before anything else sees it.
  - JSON keys are case-exact, every read key and string must print as it is,
    and size, nesting and entry counts are capped (size before decoding, nesting while it
    decodes, entries before any is read).
  - No catalog field runs a command; entries that declare components, headers or a
    `headersHelper` are skipped.
  - Relative entries stay inside the fetched tree, which holds no links.
  - `market.Entry.Spec` gives a source a catalog names (every entry but a relative one)
    `Spec.FromCatalog`, under which no redirect, of an archive download or of git, can take the
    fetch to a host the address rule refuses, and an archive's listed `sha256`, checked
    before extraction.
  - Residual: host names are never resolved.
  - **Known limit** (decided 2026-10-22): after install the stricter redirect rule follows an
    entry only through its `market:`; `lola sync` applies it to an entry whose `market` names
    another source than its own. These fetch under the rule of a source you typed:
    - `sync` and `update` of an entry without `market` (which fetch a pinned version from
      `lock.json`);
    - `lola cache update` (from the cache's record);
    - an install by cached name that fetches again (from the cache's record, when the cache has
      no head for the ref);
    - an entry installed with `--ref` (which records no `market:`).
  - A server that later redirects such a source can lead those fetches to an internal host.
  - `sync --update` fetches a catalog only for entries the trust record covered before the run.
- **Recorded sources.** `project.yml` and `lock.json` may be someone else's: their sources,
  refs, paths, commits and digests are checked when they are loaded, before any git run, for
  every command that reads them (`list` included). Names,
  paths, manifest metadata and runtime hook fields pass through `untrusted.Text` before lola
  prints them; other text a module contains (frontmatter keys quoted in finding texts, for
  example) is not yet sanitized.
- **Pulled manifests.** An entry of `project.yml` lola did not write (a pull, a hand edit) is
  never installed by name or rewritten: see [Trust record](#trust-record).
  - `lola sync` shows every change with the content it brings and acts only on a yes or
    `--trust`, never `-y`; off a terminal it refuses before any network contact.
  - Uncovered local sources, and covered ones written inside the project, must stay inside it,
    checked before the diff.
  - A covered folder whose content is not the version installed is a trust change.
  - Untrusted content stays in the run's staging until the yes, so a dry run or a No caches none
    of it. Residual risk: in an interactive or dry run the untrusted content is fetched (from a
    validated location) before the question, so the diff can show it.
  - A sync still renders installed modules at the versions `lock.json` records, which a pull cannot
    change because the ledger is private, and a tracked `lock.json` is refused by `sync`, `update`,
    `install`, `uninstall`, `list` and `lola profile` (see **Tracked ledger** in [Trust
    record](#trust-record)).
  - Step paths in `--json` output stay raw (JSON-escaped): `lola migrate` reads them as paths. So
    do `lola tidy`'s item `path` and `trash`, which scripts restore from.
  - Every other string `lola tidy` prints went through `untrusted.Text`, and MCP servers show only
    their server label (a program's base name or a URL's host), never arguments, environment,
    headers or a URL's other parts.
- **Tidy** never deletes: what it takes is in the trash.
  - It moves only direct entries of declared destinations and direct members of declared server
    maps.
  - Never into a destination a link redirects onto the root, another destination, git's or lola's
    files or another project.
  - Never an instruction file.
  - Residual: the rename into the trash is path-based (`os.Root.Rename` works within one root and
    `renameat` would need a new dependency), so a process running as you, or a git checkout, that
    swaps a path component between the re-check and the rename can redirect it; across file
    systems such a process can change an entry between the fingerprint and its removal.
- **Templates are untrusted input.** They render in the sandbox of [Templates](#templates): no
  file, environment, network or Go method is reachable, every value and the output are capped,
  and the output is checked and consented as a literal file with those bytes would be. The
  vendored liquid copy is outside `govulncheck`'s reach; its advisories are checked by hand
  ([release.md](../release.md), item 3).
- **`pins.yaml` is synced input.** It travels through a dotfile manager, so lola reads it as it
  reads any file it did not write on this machine:
  - strictly validated before use (names, sources by the host rules of a remote profile, digests; a
    local path or `file://` source is refused), with `path:line` refusals before anything is
    fetched.
  - A restored remote profile is fetched only from a source those rules allow, checked before the
    network, and written only when the commit and the digest equal the record; otherwise the run
    stops.
  - A pin that differs from the installed copy is a change shown in the plan, never an unasked
    upgrade.
  - A file changed between lola's read and its write is refused.
- **Unparseable files** are never rewritten, `selections.yaml` and `trust.json` included.
- **User files are untrusted.** `config.yaml` and `selections.yaml` are size-capped and strictly
  validated; their keys and values reach the terminal only through `untrusted.Text`.
- lola's own state files are `0600`; its XDG directories `0700`.
