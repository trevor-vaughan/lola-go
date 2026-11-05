# Walkthroughs

Worked examples of profile commands. It is split out of [profiles](profiles.md).

Each flow is a scenario of the `profile-first` end-to-end suite (`e2e/profile-first`, S1-S8),
run against real git repositories with fixture sources and the flags a script needs (`-y`, `-C`).
Two commands are not run as written: `--move my-notes --to me --switch` appears only as the hint
lola prints, and `--move my-notes --to base` is run with `-y -n` (a dry run).

- **A first install.** `lola install <source>` from inside a project, answer with user scope: it
  writes your `default` profile and says `user scope: default`, never "profile".
- **A baseline and a mode.** `lola profile go-review --switch --new --user`, then `lola install
  <source> --user` puts a module in `go-review`. `lola profile --edit <module> --user` on a module
  that `default` provides asks first where the change goes (`Override it in profile go-review`),
  then shows the component page titled `profile go-review › cc-skills-golang › skills (7)`. Back
  to the baseline: `lola profile --off --user -y`.
- **An experiment.** `lola profile try --switch --new --from go-review` copies a profile and turns
  it on; `lola profile go-review --switch -y` goes back and leaves `try` as it was.
- **The wrong place.** A module installed into the shared `project.yml` moves to a profile of
  yours: `lola profile --move my-notes --to me --switch`, or `lola profile --user --move my-notes
  --to base` for every project. The move prints the target scope's context line first, then the
  source's.
- **A team project.** A teammate runs `lola sync`, then `lola profile me --switch --new`: what
  they install lands in `me` in their home, and git has nothing to show.
- **Combining.** `lola profile work --switch --new --bundle go-review,docs --user`; the later
  member wins an overlap and a notice says so. `lola install <source> --user` asks which member
  gets the module.
- **One glance.** `lola profile` names each scope's profile and every profile's state.
- **A new machine.** Copy `~/.config/lola`, run `lola sync --user`, then `lola profile go-work
  --switch --user`: it installs the versions the first machine has, though the source has moved
  on since, and restores a remote profile from `pins.yaml`. See [Syncing lola between
  machines](sync-and-trust.md#syncing-lola-between-machines).
