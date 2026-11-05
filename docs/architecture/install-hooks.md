# Install hooks

Maintainer notes on Install hooks. It is split out of [architecture](../architecture.md).

`pre-install` and `post-install` scripts must be executable regular files inside the module (checked
when the module is loaded).

After the plan is computed and before anything is written, the engine decides consent per module: a
recorded consent for the same digest (sha256 over each hook's event, script path and script sha256),
a `--trust=hooks` (its grant recorded only after the receipt, [Trust kinds](#trust-kinds)), or the
CLI's prompt, whose yes is recorded in `$XDG_STATE_HOME/lola/consent.json` with the run's grants,
after its receipt is answered yes and before the first write. Without consent a `pre-install` hook
is an error and a `post-install` hook is declined with a warning.

Hooks run once per target, in the module copy, with the parent environment plus `LOLA_MODULE_NAME`,
`LOLA_MODULE_PATH`, `LOLA_PROJECT_PATH` (the target's scope root), `LOLA_ASSISTANT`, `LOLA_SCOPE`
and `LOLA_HOOK`, under a 10-minute timeout. A failing `pre-install` hook restores the old copies and
writes nothing; `post-install` hooks run after the commit, so a failure is a warning.

`install` runs hooks for every module it installs that has consent (a module whose copy it keeps,
not pending, asks nothing off a terminal and is not refused for a `pre-install` hook); `update`, or
a sync that only re-renders, only for modules whose copy it replaces (a new version, or a missing
copy). A dry run reports hooks as `would-run` (`would run` in human output).

A `post-install` hook declined for want of consent is **pending**: `prepare` sets
`ledger.Entry.HooksPending` for a module whose post-install hook it declined, clears it for one
whose hooks run, and keeps it for a module whose hooks the run does not reach.

Consent is keyed on the scripts of the hashed version (`hookScripts(c.src, …)`) while hooks run in
the module copy, which `needsCopy` never compares, so a hook must never run in the kept copy: `Sync`
plans a pending module's copy as replaced (`moduleCopy.forced`), swapped in again from the snapshot
or staged tree, and `Install` plans every copy that needs no new version and adds no target the same
way; their hooks go through `planHooks`.

When no hook job is planned for a forced copy, `keepUnhooked` undoes the replace before `commit`, so
the copy, what earlier hooks built in it and its mark stay (a sync is still in sync). A forced copy
whose hook is not pending (`moduleCopy.pending`) is skipped without a notice or a `pre-install`
refusal when consent is missing. A planted `hooks_pending` in an untracked `lock.json` therefore
runs nothing the user did not consent to for those bytes.

The declined-hook notice names `lola sync <m>`, and off a terminal `lola sync <m> --trust=hooks`,
with the scope's flags. A pending hook is offered only to a run that names its module or gives
`--trust=hooks` (`planSync`'s `named`): any other run plans the copy as it is, asks nothing, and
adds the notice `pending post-install hook for <m> → lola sync
<m>`, so an unrelated full sync never stops at the hook question.

## Install scripts

`internal/engine/scripts.go` adds the user's (`config.Config.PreInstall`/`PostInstall`, absolute
or `~/` expanded at load), the project's (`manifest.Manifest.PreInstall`/`PostInstall`, which
a profile refuses) and the run's (`Options.PreInstall`/`PostInstall`,
made absolute by the CLI against its working directory) scripts around every module copy a run
installs or replaces. `planHooks` returns a `hookJob` for each such copy, its `scripts` the hooks
that run in it (none when it has none or they were declined), so a module without hooks is
wrapped too; a forced copy whose hooks do not run gets no job, `keepUnhooked` keeps it, and
nothing wraps it.

`prepare` checks the scripts (`installScripts`) before anything is asked, when
any copy may be written, then `planScripts` decides each project script's trust once (a recorded
consent under `consent.ScriptKey(<resolved project dir>, <event>)` for `sha256:<script sha256>`,
`--trust=scripts` through the receipt, a batch's `Expect`, or `Options.AskScript`), and `commit`
runs them through `runHooks`: per job, the pre-install scripts in order user, project, flag
before its hooks, and the post-install ones in order flag, project, user after them, once per
target.

Each script is held as an absolute, resolved path from planning on: the project's refused when its
path is not plain text (a newline would forge prompt lines and split `--plain` rows), is absolute
(it would name one machine's checkout) or has a `..` component (`filepath.Join` cleans `link/..`
lexically, so lola would run another file than a shell reading the path finds), then resolved under
the canonical project root, contained in it (`source.IsWithin`), not itself a link, and shown from
then on as its path from that root beside the root; every script path is shown on one line
(`oneLine`).

The user's and the flag's resolved with `EvalSymlinks`. `runHook` executes that path (never
`filepath.Join(j.dir, …)`, so a file of the same relative name in the module copy never runs), with
`Cmd.Dir` the module copy and `LOLA_SCRIPT_SOURCE` added; a module hook's environment drops any
`LOLA_SCRIPT_SOURCE` inherited from lola's own.

The digest taken at planning is checked again (`Lstat` regular file, sha256 equal) just before each
exec; a mismatch fails that script (pre: the run stops and the copies roll back; post: a warning).
The window between that check and `exec` remains, the class module hooks have.

Project scripts are planned only when the scope is that project. A `Sync` also plans them
when it writes no copy (`Options.askScripts`), reading only the project's (`runOrder` of
`projectScripts`): an untrusted pre-install script is asked about for later runs
(`ScriptQuestion.Later`) or refused off a terminal, and its dry run's command adds
`--trust=scripts` (`Result.needs`); a post-install one is not asked about, only warned of. A
declined post-install script stays in the run's list (`installScript.declined`) so `runScripts`
records it in run order. A mark whose script project.yml no longer names is cleared.

A post-install script of the project declined for want of trust sets `ledger.Entry.ScriptsPending`
on every copy it would have wrapped; `planSync` offers a pending one (`moduleCopy.pendingScript`)
only to a run that names the module or gives `--trust=scripts`, and `planScripts` runs it alone, per
target, in the existing copy (it is not replaced: consent covers the script, not the module's bytes)
only with consent for the script's current digest; otherwise the copy keeps its mark and the notice
`pending post-install script for <m> → lola sync <m>`.

`prepare` keeps the mark of a copy whose script runs alone, and `commit` clears it, saving
`lock.json` again, only once the script ran on every target, so a failed one is offered again. A dry
run plans a pending script as the run does: counted as a change (`scriptPlan.alone`) when trusted,
else listed as needing trust with the notice.

A refusal for want of trust hints every kind the run would still be refused for: `prepare` decides
them before anything asks (`unaskedKinds`: a pre-install hook, and the project's pre-install script,
without a recorded consent, when the run cannot ask about that kind), and the hints of the hook
refusal and of the declined runtime hooks add them, so the run a hint names is not refused for the
next kind.
