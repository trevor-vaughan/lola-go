# Bundles

A bundle combines profiles into one. It is split out of [profiles](profiles.md).

A bundle combines profiles. It is a profile like any other (`--on`, `--switch`, `--off`, `lola
profile <bundle>`), whose file lists other profiles in `bundle:` instead of modules.

- **Members.** Each is a profile's name, bare or qualified (`user:`, `project:`, `remote:`).
  - In a bundle of yours a bare name is your profile, else a remote one. A project's profile is
    named `project:<name>`, which works at that project's scope only.
  - In a project's bundle a bare name is the project's profile, else a remote one, and never one of
    yours (`user:` is refused there).
  - `default` and `base` are never members: the base is always loaded.
  - A member that is no profile you have refuses at its line, hinting a command that works.
- **Order.** Members load in the order listed, depth-first through nested bundles, over the base.
  A later member's entry for a module wins whole, with a notice naming the bundle (`fmt: profile
  over overrides profile go-review in bundle dup`); `lola profile <bundle>` shows it as `(over
  profile:user:go-review)`.
  - A profile or bundle reached twice, through two paths, keeps its first place and is expanded
    once; each name that repeats gets one notice (`go appears twice in bundle work; its first place
    counts`).
  - A bundle may nest others up to 8 levels deep (the one on and seven inside it), and a bundle on
    expands to at most 64 profiles and at most 64 bundles, counted separately. A cycle (`bundle:
    a closes a cycle: a → b → a`), the depth and the caps refuse, naming the file and line.
- **Making one.** `lola profile work --switch --new --bundle go-review,docs` makes the bundle, and
  any bare member that does not exist yet as an empty profile of yours, and switches to it, in the
  one run (`✓ new profile docs … empty`, `✓ new bundle work (go-review, docs)`). A `project:`
  member must exist already: `lola profile --project project:x --switch --new` makes it, and the
  refusal hints that. `--switch --new` alone makes an empty profile; `--from <profile>` makes a copy
  of a profile's entries and targets (never a pin), and copies a bundle with its members written so
  they name the same profiles (qualified where the bare name would read as another); `--from
  project:x` copies into yours only what a [move](#moving-a-module) would carry, with the same
  trust review and the same refusal of a project's local folders. `--create` is an alias of `--new`.
  `--from` and `--bundle` together refuse (pick one), and `--new` for a name that exists hints the
  switch. Under `-n` nothing is made (`→ would make bundle work (go-review, docs)`).
  `lola profile <bundle> --edit --bundle a,b,c` changes its members later
  ([Editing a profile](#editing-a-profile)); the refusals it answers (a member that is gone, a
  bundle with no profile lola can write, `--on` of another profile) hint it.
  - `lola profile go-review docs --switch`, and `--on` with another profile on, hint the bundle that
    holds them (`lola profile go-review-docs --switch --new --bundle go-review,docs`).
- **Editing under a bundle.** An install asks which member gets the module (the last member wins
  overlaps), even with one writable member; off a terminal it refuses with a `--profile <member>`
  hint per member.
  - `--profile work` itself, naming the bundle, and `--move <module> --to work`
    refuse (`work is a bundle; it lists profiles, not modules`, `work is a bundle; move into one of
    its members`), hinting each member.
  - A bundle with no member lola can write asks whether to write
    the base.
  - An uninstall removes the module from the member it comes from, and asks before removing
    a module the base gives, as it does under any profile (`-y` answers when one layer lists it).
- **Remote profiles in a bundle.** A remote profile you use alongside your own goes in a bundle:
  `lola profile go-work --switch --new --bundle remote:go,go-extras`. Its entries keep their pins,
  which only `lola profile remote:<name> --update` changes (`lola sync --update <module>` refuses
  them). A remote member this machine lacks, which `pins.yaml` records, is left out with a warning
  and restored by the next run that needs it, as for a [remote profile](remote-profiles.md) that is
  on. For a project's bundle that restore waits until the bundle's member list is trusted.
- **Splitting a file that does both.** `lola profile work --split` moves the modules and targets
  of a file that has both keys into `work-modules` (`--to` names it another way), and makes `work`
  a bundle of its members then `work-modules`, so its own entries still win:

  ```
  ~ user:mixed  bundle: go-review, mixed-modules  (<config>/lola/profiles/mixed.yml)
  + user:mixed-modules  modules: kit2  (<config>/lola/profiles/mixed-modules.yml, new)
  ```

  - It asks first on a terminal (`-y`, `--force`); `-n` prints this plan and writes nothing.
  - Any command that reads such a file refuses with the line and the `--split` hint, and on a
    terminal offers the split in place (`Split work now?`) and runs again.
  - Entries keep the trust they had,
    but a project bundle's member list is asked about afresh (`-y` and `--force` never answer it;
    accept it with `--trust=profiles`), because a split must not grant trust to content pulled from
    the project.
  - A remote profile cannot be split, and a file with only one of the keys has nothing
    to split.
- **Seeing it.** `lola profile work` prints `bundle of user:go-review, user:docs`; the context line
  names the bundle and its members (`user scope: bundle work (go-review, docs) over default`), and
  `--json` carries `bundle` and `members` there.
