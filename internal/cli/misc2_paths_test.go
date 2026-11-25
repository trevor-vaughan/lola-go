package cli_test

// One spec per row of the init, market add, market picker, migrate and hook and script consent
// transition tables in docs/architecture/tui-flows.md, named "init: <from> --<input>--> <to>",
// "market add: …", "market picker: …", "migrate: …" and "consent: …", the input followed by the
// row's guard in parentheses where rows share a from and an input. Rows an existing spec pins
// already:
//
// also pinned (init: start --run (no terminal)--> refused): confirm_test.go "off a terminal, refuses with the plan and a hint that keeps the run's flags, writing nothing"
// also pinned (init: start --run (an invalid name)--> refused): confirm_test.go "refuses an invalid name before asking anything"
// also pinned (init: confirm --yes--> done): confirm_test.go "on a terminal, shows the plan, asks, and scaffolds when you accept"
// also pinned (init: confirm --no--> cancelled): confirm_test.go "on a terminal, declining writes nothing and exits 4"
// also pinned (market add: start --run (the catalog's own name cannot name a marketplace)--> name): market_test.go "asks a name on a terminal for a catalog whose own name no marketplace can have, pre-filled with its slug"
// also pinned (market add: name --esc--> name): keys_internal_test.go "Esc on the marketplace name question stays: it is a first screen"
// also pinned (migrate: start --run (no terminal)--> refused): migrate_test.go "needs -y off a terminal, printing the plan and writing nothing"
// also pinned (migrate: root --answer--> next step; migrate: targets --answer--> next step): migrate_test.go "asks where each missing root is and which assistants stand in for each missing one, then migrates with the answers"
// also pinned (migrate: root --esc (first screen)--> root; migrate: targets --esc (the first, after a root question)--> root): migrate_test.go "goes back from an assistant question to the root question on Esc, the root answered pre-filled"
// also pinned (migrate: confirm --yes--> done): confirm_test.go "c4: on a terminal without --json, <cmd> asks after the plan and prints the human result"
// also pinned (migrate: confirm --no--> cancelled): confirm_test.go "c5: declining <cmd> on a terminal writes nothing and exits 4"
// also pinned (migrate: consent --yes--> done): misc2_paths_test.go "migrate: confirm --yes (a hook or script without your consent)--> consent"
// also pinned (consent: start --run (--json or no terminal, post-install only)--> done): scripts_test.go "an untrusted project pre script refuses off a terminal with --trust=scripts; post is declined and pending"
// also pinned (consent: hooks --esc--> hooks; consent: runtime --esc--> runtime; consent: script --esc--> script; consent: later --esc--> later): keys_paths_internal_test.go "viewer: inline --esc (no screen before)--> inline"
// also pinned (consent: hooks --q--> cancelled; consent: runtime --q--> cancelled; consent: script --q--> cancelled; consent: later --q--> cancelled): keys_paths_internal_test.go "viewer: inline --q (fewer than two answers)--> cancelled"
// also pinned (consent: later --ctrl+c--> cancelled): misc2_paths_test.go "consent: start --run (lola sync, nothing to install)--> later"
// also pinned (market picker: start --run (a terminal)--> picker; market picker: picker --esc or q--> left): marketflow_test.go "opens without contacting a host, and leaving it changes nothing"
// also pinned (market picker: start --run (a terminal)--> picker): market_picker_internal_test.go "makes no check on open and shows no status"
// also pinned (market picker: start --run (--json, --plain, --no-pager, --no-interactive or market_picker: false)--> list): marketflow_test.go "lists as text under --json, --plain, --no-pager, --no-interactive and market_picker: false, never asking"
// also pinned (market picker: start --run (no terminal)--> list): marketflow_test.go "lists as text off a terminal"
// also pinned (market picker: start --run (no terminal)--> list): market_picker_internal_test.go "never asks off a terminal"
// also pinned (market picker: start --run (accessible mode)--> numbers; market picker: numbers --answer--> next step): market_picker_internal_test.go "lists the rows numbered, with no status, and answers the numbers chosen"
// also pinned (market picker: numbers --answer (refused)--> numbers): market_picker_internal_test.go "asks again after an answer it cannot take"
// also pinned (market picker: numbers --Add a source…'s number--> source; market picker: numbers --empty submit--> left): market_picker_internal_test.go "adds a source for Add a source…'s number, and cancels on an empty answer"
// also pinned (market picker: numbers --Add a source…'s number--> source): market_picker_internal_test.go "asks Add a source…'s source and ref on line prompts, trimmed, and refuses when input ends"
// also pinned (market picker: numbers --end of input--> refused): market_picker_internal_test.go "refuses when input ends"
// also pinned (market picker: picker --x--> picker; market picker: picker --ctrl+a--> picker): market_picker_internal_test.go "ticks with x, all and none with ctrl+a, and answers the ticks in row order on Enter"
// also pinned (market picker: picker --/--> filter; market picker: filter --answer--> picker; market picker: picker --esc (a filter kept)--> picker): market_picker_internal_test.go "narrows the list with / and takes a, u, r and q typed there as filter text"
// also pinned (market picker: picker --o (the row has a web address)--> picker; market picker: picker --o (the row has no web address)--> picker; market picker: picker --o (the cursor on Add a source…)--> picker; market picker: picker --o (the address is not a plain http(s) one)--> picker): market_picker_readable_internal_test.go "the marketplace picker's o key on the list"
// also pinned (market picker: numbers --o<N>--> numbers): market_picker_readable_internal_test.go "the marketplace picker's o key on the list in accessible mode"
// also pinned (market picker: filter --answer--> picker): market_picker_internal_test.go "takes U and u typed in the filter as text"
// also pinned (market picker: filter --answer (an offer waiting)--> offer): market_picker_internal_test.go "waits for a filter being typed to end before it offers"
// also pinned (market picker: picker --u--> picker): market_picker_internal_test.go "checks the cursor row on u when nothing is ticked, and the ticked checkable rows when some are"
// also pinned (market picker: picker --u--> picker): market_picker_internal_test.go "checks on u, git rows reading checking…, and a result updates its row alone"
// also pinned (market picker: picker --u--> picker): marketflow_test.go "checks the remotes of the rows the picker gives only when it starts a check, as u does"
// also pinned (market picker: picker --u (nothing ticked, the cursor on Add a source…)--> picker): market_picker_internal_test.go "asks for a row on u with nothing ticked and the cursor on Add a source…"
// also pinned (market picker: picker --u (a check running)--> picker): market_picker_internal_test.go "re-checks on a second u, cancelling the first run and dropping its results"
// also pinned (market picker: picker --u (only rows of yours it cannot check)--> offer): market_picker_internal_test.go "offers yours it cannot check at once, contacting no host"
// also pinned (market picker: picker --u (no row it can check or offer)--> picker): market_picker_internal_test.go "says nothing to update, not up to date, when u checked nothing"
// also pinned (market picker: picker --U--> picker): market_picker_internal_test.go "checks every row on U, ticked or not and whatever the filter shows, and offers those of yours that need it"
// also pinned (market picker: picker --U (a check running)--> picker): market_picker_internal_test.go "re-checks on U after u, cancelling the first run and dropping its results, and stops on leaving"
// also pinned (market picker: picker --checks end (rows of yours to update)--> offer): market_picker_internal_test.go "offers, once the check ends, to update the checked rows that need it and yours it cannot check"
// also pinned (market picker: picker --checks end (rows of yours to update)--> offer): market_picker_internal_test.go "offers only the rows that need it, never those up to date or unreachable"
// also pinned (market picker: picker --checks end (rows of yours to update)--> offer): market_picker_internal_test.go "keeps the offer to one line at any width, its keys always shown, the list still in view"
// also pinned (market picker: picker --checks end (at least two failed, and more than half of those checked)--> picker): market_picker_internal_test.go "shows the network banner, and rows not checked, when two of three checks fail, and offers nothing"
// also pinned (market picker: picker --checks end (at least two failed, and more than half of those checked)--> picker): market_picker_internal_test.go "offers nothing on U under the network banner"
// also pinned (market picker: picker --checks end (nothing to update)--> picker): market_picker_internal_test.go "says when nothing checked needs updating, and stays open"
// also pinned (market picker: picker --checks end (a row of yours failed, no banner)--> picker): market_picker_internal_test.go "shows one unreachable row with its reason and both hints"
// also pinned (market picker: picker --checks end (a known row you have not enabled failed, no banner)--> picker): market_picker_internal_test.go "hints u checks again, not --remove, for an unreachable known marketplace you have not enabled"
// also pinned (market picker: picker --checks end (the only check failed)--> picker): market_picker_internal_test.go "names the connection too when the only check fails"
// also pinned (market picker: offer --yes--> picker): market_picker_internal_test.go "offers, once the check ends, to update the checked rows that need it and yours it cannot check"
// also pinned (market picker: offer --yes--> picker): marketflow_test.go "updates your own marketplaces on u, as --update does, and opens again"
// also pinned (market picker: offer --yes (one registered at a commit)--> move; market picker: move --yes--> picker; market picker: move --no--> picker): marketflow_test.go "updates a known marketplace past lola's pin on u after asking, registering the remote's tip", "moves past the commit on u after asking, after which it reads up to date"
// also pinned (market picker: offer --no--> picker): market_picker_internal_test.go "declines with esc or n at the offer and stays open with the statuses shown"
// also pinned (market picker: offer --u or U--> picker): market_picker_internal_test.go "drops the update offer and checks again on u or U, as the unreachable row's hint says"
// also pinned (market picker: offer --another key--> offer; market picker: offer --q--> left; market picker: offer --ctrl+c--> cancelled): market_picker_internal_test.go "ignores other keys at the offer, and q or Ctrl+C there change nothing"
// also pinned (market picker: picker --Enter (known rows ticked)--> enable; market picker: enable --yes--> picker; market picker: enable --no--> picker): marketflow_test.go "enables a ticked known marketplace at its pin after the --enable question, and opens again with it among yours"
// also pinned (market picker: picker --Enter (rows of yours ticked)--> action; market picker: action --Remove--> remove; market picker: remove --yes--> picker; market picker: remove --no--> picker): marketflow_test.go "removes a ticked marketplace of yours only when the --remove question is answered yes"
// also pinned (market picker: picker --Enter (rows of yours and known ones ticked)--> enable; market picker: action --Update--> picker): marketflow_test.go "on mixed ticks enables the known ones first, then asks what to do with yours"
// also pinned (market picker: picker --Enter (on Add a source…)--> source; market picker: picker --a--> source): market_picker_internal_test.go "adds a source with a, or Enter on Add a source…"
// also pinned (market picker: picker --a--> source; market picker: source --answer--> picker; market picker: source --esc--> picker): marketflow_test.go "adds a source on a, asking --add's questions, and opens again with the new marketplace ticked"
// also pinned (market picker: source --answer--> picker): market_picker_internal_test.go "ticks the rows Preselect names on open"
// also pinned (market picker: source --ctrl+c--> cancelled): marketflow_test.go "ends the run on Ctrl+C in Add a source…'s source and in --add's name question"
// also pinned (market picker: picker --esc or q--> left; market picker: picker --ctrl+c--> cancelled): market_picker_internal_test.go "cancels with esc and q, and Ctrl+C aborts"
// also pinned (market picker: picker --esc or q--> left; market picker: picker --ctrl+c--> cancelled): market_picker_internal_test.go "stops its checks on leaving and reads no result after"
// also pinned (market picker: picker --ctrl+c--> cancelled): marketflow_test.go "ends the run on Ctrl+C in the picker, changing nothing"
// also pinned (market picker: action --Browse entries--> browse; market picker: browse --answer--> picker): marketflow_test.go "browses the entries of the ticked marketplaces of yours through lola install's picker"
// also pinned (market picker: browse --ctrl+c--> cancelled): marketflow_test.go "ends the run on Ctrl+C in the install flow that browses entries"
// also pinned (market picker: action --ctrl+c--> cancelled; market picker: action --Cancel--> picker; market picker: action --esc--> picker): marketflow_test.go "ends the run on Ctrl+C in Do what with these?, and goes back to the picker on its Cancel"
// also pinned (market picker: start --run (--scope both)--> refused): marketflow_test.go "refuses --scope both before opening, since it acts on one scope"
// also pinned (market picker: picker --Enter (nothing ticked, a row of yours with entries)--> entries): market_picker_internal_test.go "opens a marketplace's entries on Enter with nothing ticked, under the context line"
// also pinned (market picker: picker --Enter (nothing ticked, a row of yours without entries)--> picker): market_picker_internal_test.go "acts on a known row on m, and says a row without entries has none"
// also pinned (market picker: picker --m--> next step): market_picker_internal_test.go "keeps the menu on Enter with a row ticked, and on m"
// also pinned (market picker: entries --/--> entryfilter; market picker: entryfilter --answer--> entries; market picker: entries --esc (a filter kept)--> entries): market_picker_internal_test.go "filters entries by name, description and tag, shows none for no match, and opens nothing then"
// also pinned (market picker: entries --Enter--> detail; market picker: entries --esc--> picker; market picker: detail --esc--> entries): market_picker_internal_test.go "goes back a page on Esc, restoring the cursor and the filter, and cancels from the list"
// also pinned (market picker: entries --q--> left; market picker: entries --ctrl+c--> cancelled; market picker: detail --q--> left; market picker: detail --ctrl+c--> cancelled): market_picker_internal_test.go "quits on q at either page and aborts on Ctrl+C"
// also pinned (market picker: detail --i (not installed)--> handoff; market picker: detail --d (installed)--> handoff; market picker: detail --i (installed)--> detail; market picker: detail --d (not installed)--> detail): market_picker_internal_test.go "draws an entry's detail, and refuses i on an installed one and d on one that is not"
// also pinned (market picker: handoff --answer (begun on the detail page)--> detail): marketflow_test.go "installs where the install flow's own scope question is answered, and opens again at the entry, installed"
// also pinned (market picker: handoff --answer (begun on the detail page)--> detail): marketflow_test.go "uninstalls an installed entry and opens again at it, not installed"
// also pinned (market picker: handoff --answer (begun on the detail page, the action failed)--> detail): marketflow_test.go "prints a failed install's error with its hints and opens again at the same entry"
// also pinned (market picker: handoff --answer (begun on the entries page)--> entries): marketflow_test.go "opens again on the entries page when the install began there"
// also pinned (market picker: handoff --answer (begun on the entries page, the action failed)--> entries): marketflow_test.go "prints a failed install's error and opens again on the entries page when it began there"
// also pinned (market picker: entries --i (not installed)--> handoff; market picker: entries --d (installed)--> handoff; market picker: entries --i (installed)--> entries; market picker: entries --d (not installed)--> entries): market_picker_internal_test.go "installs and uninstalls from the entries page, saying why when the key does not apply"
// also pinned (market picker: entries --i (not installed)--> handoff; market picker: entries --d (installed)--> handoff): market_picker_internal_test.go "acts on the entry the filter leaves under the cursor, and on none when it shows none"
// also pinned (market picker: picker --→ (nothing ticked, a row of yours with entries)--> entries; market picker: picker --→ (rows ticked, Add a source… or a row without entries)--> picker; market picker: entries --→--> detail; market picker: detail --→--> detail): market_picker_internal_test.go "goes in a level on Right, only where Enter would drill or open"
// also pinned (market picker: picker --←--> picker; market picker: entries --←--> picker; market picker: detail --←--> entries): market_picker_internal_test.go "goes back a level on Left, restoring the list's cursor and filter, and never cancels from the list"
// also pinned (market picker: entryfilter --← or →--> entryfilter): market_picker_internal_test.go "ignores Left and Right while a filter is typed"
// also pinned (market picker: entries --o (the entry has a web page)--> entries; market picker: detail --o (the entry has a web page)--> detail): market_picker_internal_test.go "launches the address from a command, not from the key, then says it opened it"
// also pinned (market picker: entries --o (the entry has no web page)--> entries): market_picker_internal_test.go "says an entry with no web page has none, and launches nothing"
// also pinned (market picker: entries --o (the address is not a plain http(s) one)--> entries): market_picker_internal_test.go "refuses an address that is not a plain http(s) one, launching nothing"
// also pinned (market picker: entries --o (the browser does not start)--> entries; market picker: detail --o (the browser does not start)--> detail): market_picker_internal_test.go "says what failed and the address when the launcher fails"
// also pinned (market picker: detail --o (the entry has no web page)--> detail): market_picker_internal_test.go "says an entry with no web page has none, and launches nothing"
// also pinned (market picker: detail --o (the address is not a plain http(s) one)--> detail): market_picker_internal_test.go "refuses an address that is not a plain http(s) one, launching nothing"
// also pinned (market picker: handoff --ctrl+c--> cancelled): marketflow_test.go "ends the run on Ctrl+C in the install's own question"
// also pinned (market picker: numbers --answer (one number, a row of yours with entries)--> entries; market picker: entries --answer (accessible mode, a number from 1 to N)--> detail; market picker: detail --answer (accessible mode, i or d)--> handoff): market_picker_internal_test.go "drills into an entry's detail and uninstalls it"
// also pinned (market picker: numbers --answer (one number, a row of yours without entries)--> numbers): market_picker_internal_test.go "says a marketplace has no entries and asks again"
// also pinned (market picker: numbers --m<N>--> next step): market_picker_internal_test.go "keeps m<N> for the menu answer"
// also pinned (market picker: entries --empty submit (accessible mode)--> numbers; market picker: detail --empty submit (accessible mode)--> entries): market_picker_internal_test.go "goes back one level on an empty answer and then cancels"
// also pinned (market picker: entries --end of input (accessible mode)--> refused; market picker: detail --end of input (accessible mode)--> refused): market_picker_internal_test.go "refuses when input ends at the entries or the detail prompt"
// also pinned (market picker: picker --Enter (nothing ticked, a known row not yet previewed)--> fetch; market picker: picker --→ (nothing ticked, a known row)--> fetch): market_picker_preview_internal_test.go "starts the fetch from a command on Enter, never in Update, and says plainly what is going on"
// also pinned (market picker: picker --→ (nothing ticked, a known row)--> fetch): market_picker_preview_internal_test.go "starts the fetch on Right too"
// also pinned (market picker: fetch --result (the catalog has entries)--> entries): market_picker_preview_internal_test.go "opens the entries in preview under a header that says it is not added, above the context line"
// also pinned (market picker: fetch --result (the fetch failed)--> picker): market_picker_preview_internal_test.go "stays on the list with the real reason and a working hint when the fetch fails, and opens no entries page"
// also pinned (market picker: fetch --result (the catalog has no entry)--> picker): market_picker_preview_internal_test.go "stays on the list, saying so, when the pinned catalog offers no entry"
// also pinned (market picker: fetch --esc--> picker): market_picker_preview_internal_test.go "cancels the fetch on Esc, stays on the list, and drops a result that still arrives"
// also pinned (market picker: fetch --q--> left; market picker: fetch --ctrl+c--> cancelled): market_picker_preview_internal_test.go "cancels the fetch when the picker is quit with q or Ctrl+C"
// also pinned (market picker: fetch --Enter (the cursor on a row of yours with entries)--> entries): market_picker_preview_internal_test.go "cancels the fetch when you open one of yours, and a result that arrives later changes nothing"
// also pinned (market picker: fetch --Enter (the cursor on a row of yours without entries)--> picker): market_picker_preview_internal_test.go "says a fetch it cancelled by opening a row of yours without entries"
// also pinned (market picker: fetch --Enter (the cursor on a known row already previewed)--> entries): market_picker_preview_internal_test.go "opens a preview kept from before without a running fetch of another row taking over the screen"
// also pinned (market picker: fetch --result (the filter is typed or an update is offered)--> picker): market_picker_preview_internal_test.go "keeps a result in the session, saying so, when the picker is no longer on the plain list"
// also pinned (market picker: fetch --Enter (the cursor on another known row, nothing ticked)--> fetch): market_picker_preview_internal_test.go "cancels the fetch of one known row when another is opened, and starts that one's"
// also pinned (market picker: picker --Enter (nothing ticked, a known row already previewed)--> entries): market_picker_preview_internal_test.go "keeps the preview for the session: going back and in again fetches nothing"
// also pinned (market picker: entries --i or d (a preview)--> entries; market picker: detail --i or d (a preview)--> detail): market_picker_preview_internal_test.go "answers i and d with a note, on the entries page and the detail page, and never leaves the picker"
// also pinned (market picker: entries --m (a preview)--> enable; market picker: detail --m (a preview)--> enable): market_picker_preview_internal_test.go "answers m on the entries page and on the detail page as m on the known row does"
// also pinned (market picker: entries --x or space (not a preview)--> entries): market_picker_ticks_internal_test.go "draws a checkbox before each entry, its description two spaces past its name, ticked with x and space"
// also pinned (market picker: entries --ctrl+a (not a preview)--> entries): market_picker_ticks_internal_test.go "ticks all the entries the filter shows with ctrl+a, then none, and keeps ticks across a filter and a detail page"
// also pinned (market picker: entries --x or space (a preview)--> entries): market_picker_ticks_internal_test.go "offers no checkbox in a preview, and ignores x there"
// also pinned (market picker: entries --i (entries ticked, one not installed)--> handoff): market_picker_ticks_internal_test.go "answers every ticked entry in page order on i, ticked or not installed, the first as Entry"
// also pinned (market picker: entries --i (entries ticked, all installed)--> entries): market_picker_ticks_internal_test.go "says every ticked entry is installed on i, and runs nothing"
// also pinned (market picker: entries --i or d (entries ticked, all hidden by the filter)--> entries): market_picker_ticks_internal_test.go "says ticked entries are hidden, runs nothing and keeps the ticks when none is shown"
// also pinned (market picker: entries --d (entries ticked, one installed)--> handoff; market picker: entries --d (entries ticked, none installed)--> entries): market_picker_ticks_internal_test.go "answers the ticked entries that are installed on d, and says when none is, running nothing"
// also pinned (market picker: entries --answer (accessible mode, i<N,N…> of entries, one not installed)--> handoff): market_picker_ticks_internal_test.go "installs the entries i names, in page order, with the first as Entry"
// also pinned (market picker: entries --answer (accessible mode, i<N,N…> of entries, all installed)--> entries): market_picker_ticks_internal_test.go "says every entry i names is installed, and asks again"
// also pinned (market picker: entries --answer (accessible mode, d<N,N…> of entries, one installed)--> handoff; market picker: entries --answer (accessible mode, d<N,N…> of entries, none installed)--> entries): market_picker_ticks_internal_test.go "uninstalls the installed ones d names, and says when none is"
// also pinned (market picker: entries --answer (accessible mode, i or d with a list that is empty, has a number out of range or twice)--> entries): market_picker_ticks_internal_test.go "refuses an out of range or repeated number, an empty list and a list for o, then asks again"
// also pinned (market picker: entries --s (the catalog skipped entries)--> skipped): market_picker_skipped_internal_test.go "opens a read-only page on s: the title, then each entry's name with its reason two spaces under it"
// also pinned (market picker: entries --s (the catalog skipped none)--> entries): market_picker_skipped_internal_test.go "says nothing, and takes no s, when nothing was skipped"
// also pinned (market picker: entries --answer (accessible mode, s) (the catalog skipped entries)--> entries): market_picker_skipped_internal_test.go "prints the page on s, then asks the entries prompt again"
// also pinned (market picker: skipped --↑ or ↓--> skipped): market_picker_skipped_internal_test.go "scrolls with the arrows, g and G, never past either end, and q and ctrl+c leave as elsewhere"
// also pinned (market picker: skipped --esc--> entries; market picker: skipped --←--> entries): market_picker_skipped_internal_test.go "leaves with Esc or Left for the entries page, the cursor and the scroll as they were"
// also pinned (market picker: skipped --q--> left; market picker: skipped --ctrl+c--> cancelled): market_picker_skipped_internal_test.go "scrolls with the arrows, g and G, never past either end, and q and ctrl+c leave as elsewhere"
// also pinned (market picker: entries --answer (accessible mode, i<N> of an entry not installed)--> handoff; market picker: entries --answer (accessible mode, d<N> of an entry installed)--> handoff): market_picker_lines_internal_test.go "installs entry N with i<N>, uninstalls with d<N>, as the TUI answers from the entries page"
// also pinned (market picker: entries --answer (accessible mode, a key that does not apply to entry N)--> entries): market_picker_lines_internal_test.go "says why a letter does not apply to its entry, and asks again"
// also pinned (market picker: entries --answer (accessible mode, o<N> of an entry with a web page)--> entries): market_picker_lines_internal_test.go "opens an entry's page with o<N> through the step's opener, printing the address"
// also pinned (market picker: entries --answer (accessible mode, a letter alone, a bad number or another letter)--> entries): market_picker_lines_internal_test.go "refuses a letter with no number, a number out of range, a bad letter, and trailing text, then asks again"
// also pinned (market picker: entries --answer (accessible mode, a preview, i<N> or d<N>)--> entries; market picker: entries --answer (accessible mode, a preview, m)--> enable; market picker: detail --answer (accessible mode, a preview, m)--> enable): market_picker_lines_internal_test.go "says i and d do not apply, and enables with m at the entries prompt and at the detail prompt"
// also pinned (market picker: detail --answer (accessible mode, o)--> detail): market_picker_lines_internal_test.go "offers o next to the letter that applies, and opens the page as the entries prompt does"
// also pinned (market picker: entries --ctrl+c (accessible mode)--> cancelled): market_picker_lines_internal_test.go "ends at end of input and at Ctrl+C"
// also pinned (market picker: numbers --answer (one number, a known row)--> entries): market_picker_lines_internal_test.go "prints the status line before the fetch starts, then the preview under its header"
// also pinned (market picker: numbers --answer (one number, a known row, the fetch failed)--> numbers): market_picker_lines_internal_test.go "prints why a fetch failed with its first hint and asks the market prompt again"
// also pinned (market picker: numbers --answer (one number, a known row, the catalog has no entry)--> numbers): market_picker_lines_internal_test.go "says a catalog with no entry offers none, and stays on the list"
// also pinned (market picker: numbers --ctrl+c (a known row's fetch running)--> cancelled): market_picker_lines_internal_test.go "cancels the fetch on Ctrl+C and aborts, without waiting for it"

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("market add paths", func() {
	var tmp, acme, priv string
	var p paths.Paths
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		GinkgoT().Chdir(tmp)
		acme, priv = filepath.Join(tmp, "acme"), filepath.Join(tmp, "priv")
		writeTree(acme, map[string]string{".claude-plugin/marketplace.json": marketCatalog, "tools/formatter/x.md": "x"})
		writeTree(priv, map[string]string{".claude-plugin/marketplace.json": sshMarketCatalog})
	})

	// markets is markets.yaml as written.
	markets := func() string {
		GinkgoHelper()
		b, err := os.ReadFile(p.Markets)
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}

	It("market add: start --run--> done", func() {
		sp := &scripted{}
		r := lolaP(sp, "market", "--add", acme)
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(BeEmpty(), "a free name and no ssh host: nothing asked")
		Expect(r.out).To(HavePrefix("✓ marketplace acme: 2 entries (Claude marketplace.json) from " + acme + "\n"))
		Expect(markets()).To(ContainSubstring("- name: acme\n"))
		Expect(filepath.Join(p.MarketCache, "acme")).To(BeADirectory())
	})

	It("market add: start --run (the name is another source's)--> name", func() {
		Expect(lola("market", "priv", "--add", acme).code).To(Equal(0))
		before := markets()
		sp := &scripted{names: []string{"mine"}, sshHosts: []func(cli.SSHHostStep) ([]string, error){aborted[cli.SSHHostStep, []string]}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(4), r.err)
		Expect(sp.calls).To(Equal([]string{"market-name", "ssh-hosts"}))
		Expect(sp.nameSteps[0]).To(Equal(cli.MarketNameStep{Why: "priv is already a marketplace, from " + acme, Value: "priv-2"}))
		Expect(markets()).To(Equal(before))
	})

	It("market add: start --run (the name is this marketplace already)--> refused", func() {
		Expect(lola("market", "--add", acme).code).To(Equal(0))
		before := markets()
		sp := &scripted{}
		r := lolaP(sp, "market", "acme", "--add", acme)
		Expect(r.code).To(Equal(1))
		Expect(sp.calls).To(BeEmpty())
		Expect(r.err).To(Equal("error: acme is already this marketplace\n  hint: lola market acme --update\n"))
		Expect(markets()).To(Equal(before))
	})

	It("market add: start --run (ssh hosts not allowed)--> ssh", func() {
		sp := &scripted{sshHosts: []func(cli.SSHHostStep) ([]string, error){aborted[cli.SSHHostStep, []string]}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(4), r.err)
		Expect(sp.calls).To(Equal([]string{"ssh-hosts"}))
		Expect(sp.sshSteps[0].Market).To(Equal("priv"))
		Expect(sp.sshSteps[0].Hosts).To(Equal([]cli.SSHHostChoice{{Host: "git.corp.test", Entries: 2}, {Host: "other.test", Entries: 1}}))
		Expect(p.Markets).NotTo(BeAnExistingFile())
	})

	It("market add: start --run (--ssh-host)--> ssh", func() {
		sp := &scripted{sshHosts: []func(cli.SSHHostStep) ([]string, error){func(cli.SSHHostStep) ([]string, error) { return nil, nil }}}
		r := lolaP(sp, "market", "--add", priv, "--ssh-host", "git.corp.test")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.sshSteps[0].Hosts).To(Equal([]cli.SSHHostChoice{{Host: "other.test", Entries: 1}}), "only the hosts --ssh-host left")
		Expect(markets()).To(ContainSubstring("ssh_hosts:\n      - git.corp.test\n"))
		sp = &scripted{}
		r = lolaP(sp, "market", "both", "--add", priv, "--ssh-host", "git.corp.test", "--ssh-host", "other.test")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(BeEmpty(), "nothing left to ask")
	})

	It("market add: start --run (no terminal or --json, a taken name)--> refused", func() {
		Expect(lola("market", "priv", "--add", acme).code).To(Equal(0))
		before := markets()
		sp := &scripted{}
		r := lolaP(sp, "market", "--add", priv, "--json")
		Expect(r.code).To(Equal(1))
		Expect(sp.calls).To(BeEmpty())
		Expect(r.err).To(MatchJSON(`{"error": "priv is already a marketplace, from ` + acme + `",
			"hints": ["lola market priv-2 --add ` + priv + ` --path .claude-plugin/marketplace.json --json"]}`))
		Expect(markets()).To(Equal(before))
	})

	It("market add: start --run (no terminal or --json, ssh hosts)--> done", func() {
		sp := &scripted{}
		r := lolaP(sp, "market", "--add", priv, "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(BeEmpty())
		Expect(r.out).To(ContainSubstring("lola market priv --update --ssh-host git.corp.test --ssh-host other.test --json"))
		Expect(markets()).NotTo(ContainSubstring("ssh_hosts"))
	})

	It("market add: start --run (-n)--> done", func() {
		Expect(lola("market", "priv", "--add", acme).code).To(Equal(0))
		before := markets()
		sp := &scripted{}
		r := lolaP(sp, "market", "other", "--add", priv, "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(BeEmpty(), "a dry run asks nothing")
		Expect(r.out).To(HavePrefix("→ would add marketplace other: "))
		r = lolaP(sp, "market", "--add", priv, "-n")
		Expect(r.code).To(Equal(1), "a taken name is refused as off a terminal")
		Expect(sp.calls).To(BeEmpty())
		Expect(markets()).To(Equal(before))
		Expect(filepath.Join(p.MarketCache, "other")).NotTo(BeAnExistingFile())
	})

	It("market add: name --answer--> ssh", func() {
		Expect(lola("market", "priv", "--add", acme).code).To(Equal(0))
		sp := &scripted{names: []string{"mine"}, sshHosts: []func(cli.SSHHostStep) ([]string, error){aborted[cli.SSHHostStep, []string]}}
		Expect(lolaP(sp, "market", "--add", priv).code).To(Equal(4))
		Expect(sp.calls).To(Equal([]string{"market-name", "ssh-hosts"}))
		Expect(sp.sshSteps[0].Market).To(Equal("mine"))
	})

	It("market add: name --answer (no ssh host to ask)--> done", func() {
		Expect(lola("market", "--add", acme).code).To(Equal(0))
		other := filepath.Join(tmp, "other")
		writeTree(other, map[string]string{".claude-plugin/marketplace.json": marketCatalog, "tools/formatter/x.md": "x"})
		sp := &scripted{names: []string{"mine"}}
		r := lolaP(sp, "market", "--add", other)
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"market-name"}))
		Expect(r.out).To(ContainSubstring("→ the catalog calls itself acme; it is your marketplace mine\n"))
		Expect(markets()).To(ContainSubstring("- name: mine\n"))
	})

	It("market add: name --ctrl+c--> cancelled", func() {
		Expect(lola("market", "priv", "--add", acme).code).To(Equal(0))
		before := markets()
		sp := &scripted{names: []string{cancelRun}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(4), r.err)
		Expect(sp.calls).To(Equal([]string{"market-name"}))
		Expect(markets()).To(Equal(before))
		Expect(filepath.Join(p.MarketCache, "priv-2")).NotTo(BeAnExistingFile())
	})

	It("market add: ssh --answer--> done", func() {
		sp := &scripted{sshHosts: []func(cli.SSHHostStep) ([]string, error){func(cli.SSHHostStep) ([]string, error) { return []string{"other.test"}, nil }}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ allowed ssh hosts: other.test\n"))
		Expect(markets()).To(ContainSubstring("ssh_hosts:\n      - other.test\n"))
		Expect(markets()).NotTo(ContainSubstring("git.corp.test"))
	})

	It("market add: ssh --empty submit--> done", func() {
		sp := &scripted{sshHosts: []func(cli.SSHHostStep) ([]string, error){func(cli.SSHHostStep) ([]string, error) { return nil, nil }}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(0), r.err)
		Expect(markets()).To(ContainSubstring("- name: priv\n"))
		Expect(markets()).NotTo(ContainSubstring("ssh_hosts"), "no host allowed")
		Expect(r.out + r.err).To(ContainSubstring("lola market priv --update --ssh-host git.corp.test --ssh-host other.test"))
	})

	It("market add: ssh --ctrl+c--> cancelled", func() {
		Expect(lola("market", "priv", "--add", acme).code).To(Equal(0))
		before := markets()
		sp := &scripted{names: []string{"mine"}, sshHosts: []func(cli.SSHHostStep) ([]string, error){aborted[cli.SSHHostStep, []string]}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(4), r.err)
		Expect(markets()).To(Equal(before), "the name answered is not registered")
		Expect(filepath.Join(p.MarketCache, "mine")).NotTo(BeAnExistingFile())
	})

	It("market add: name --answer (refused)--> name", func() {
		Expect(lola("market", "priv", "--add", acme).code).To(Equal(0))
		before := markets()
		sp := &scripted{names: []string{"Bad Name", "priv", "mine"}, sshHosts: []func(cli.SSHHostStep) ([]string, error){aborted[cli.SSHHostStep, []string]}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(4), r.err)
		Expect(sp.calls).To(Equal([]string{"market-name", "market-name", "market-name", "ssh-hosts"}))
		why := "priv is already a marketplace, from " + acme
		Expect(sp.nameSteps[0]).To(Equal(cli.MarketNameStep{Why: why, Value: "priv-2"}))
		Expect(sp.nameSteps[1].Value).To(Equal("Bad Name"), "what was typed is kept")
		Expect(sp.nameSteps[1].Problem).To(ContainSubstring("Bad Name"), "the note says why it was refused")
		Expect(sp.nameSteps[2]).To(Equal(cli.MarketNameStep{Why: why, Value: "priv", Problem: why}))
		Expect(markets()).To(Equal(before), "nothing is written before the last question is answered")
	})

	It("market add: ssh --esc (after the name question)--> name", func() {
		Expect(lola("market", "priv", "--add", acme).code).To(Equal(0))
		before := markets()
		sp := &scripted{names: []string{"mine", "theirs"}, sshHosts: []func(cli.SSHHostStep) ([]string, error){goneBack[cli.SSHHostStep, []string],
			func(cli.SSHHostStep) ([]string, error) { return []string{"git.corp.test"}, nil }}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"market-name", "ssh-hosts", "market-name", "ssh-hosts"}))
		Expect(sp.sshSteps[0].Nav).To(Equal(cli.Nav{Back: true, Answered: 1}), "Esc has the name question to go back to")
		Expect(sp.nameSteps[1]).To(Equal(cli.MarketNameStep{Why: "priv is already a marketplace, from " + acme, Value: "mine"}), "the typed name kept")
		Expect(sp.sshSteps[1].Market).To(Equal("theirs"))
		Expect(markets()).NotTo(ContainSubstring("mine"))
		Expect(markets()).To(ContainSubstring("- name: theirs\n"))
		Expect(markets()).To(ContainSubstring("ssh_hosts:\n      - git.corp.test\n"))
		Expect(markets()).To(HavePrefix(before[:len(before)-1]), "priv kept as it was")
	})

	It("market add: ssh --esc (first screen)--> ssh", func() {
		sp := &scripted{sshHosts: []func(cli.SSHHostStep) ([]string, error){aborted[cli.SSHHostStep, []string]}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(4), r.err)
		Expect(sp.calls).To(Equal([]string{"ssh-hosts"}))
		Expect(sp.sshSteps[0].Nav).To(Equal(cli.Nav{}), "a first screen: Esc stays")
		Expect(p.Markets).NotTo(BeAnExistingFile())
	})
})

var _ = Describe("migrate paths", func() {
	var tmp, home, proj, src, moved string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		home, proj, src = filepath.Join(tmp, "home", ".lola"), filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "review")
		writeTree(src, map[string]string{"commands/ship.md": "Ship $ARGUMENTS\n"})
		writeTree(home, map[string]string{
			"installed.yml": "version: '1.0'\ninstallations:\n- {module: review, assistant: claude-code, scope: project, project_path: " + proj +
				", commands: [ship]}\n",
			"modules/review/.lola/source.yml": "source: " + src + "\ntype: folder\n",
		})
		writeTree(proj, map[string]string{".claude/commands/ship.md": "python ship\n"})
		moved = filepath.Join(tmp, "home", "proj")
	})

	// fromAnotherMachine records the project at /Users/alice/proj, which is here at moved, and a
	// user-scope install for claude-code, which is not installed here; opencode is.
	fromAnotherMachine := func() {
		writeTree(moved, map[string]string{".claude/commands/ship.md": "python ship\n"})
		fakeAssistant(tmp, "opencode", "1.0.0")
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n" +
			"- {module: review, assistant: claude-code, scope: project, project_path: /Users/alice/proj, commands: [ship]}\n" +
			"- {module: review, assistant: claude-code, scope: user, commands: [ship]}\n"})
	}

	// fetchedOnce asserts that the run, however many times it planned, left review cached once
	// and no run directory behind in the cache's staging.
	fetchedOnce := func() {
		GinkgoHelper()
		pp, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		versions, err := os.ReadDir(filepath.Join(pp.Store, "review", "versions"))
		Expect(err).NotTo(HaveOccurred())
		Expect(versions).To(HaveLen(1), "one cached version")
		runs, err := os.ReadDir(pp.Staging)
		if !os.IsNotExist(err) {
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(runs).To(BeEmpty(), "no run directory left in staging")
	}

	// untouched asserts the run wrote nothing: the Python lola's files and home as they were.
	untouched := func() {
		GinkgoHelper()
		Expect(home).To(BeADirectory())
		Expect(read(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal("python ship\n"))
		Expect(filepath.Join(proj, ".lola")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(tmp, "state", "lola", "trash")).NotTo(BeAnExistingFile())
	}

	// userRecords records review at user scope for each of the assistants, none installed here.
	userRecords := func(assistants ...string) {
		y := "version: '1.0'\ninstallations:\n"
		for _, a := range assistants {
			y += "- {module: review, assistant: " + a + ", scope: user, commands: [ship]}\n"
		}
		writeTree(home, map[string]string{"installed.yml": y})
	}

	It("migrate: start --run--> confirm", func() {
		p := &scripted{confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Migrate?"}))
		Expect(r.err).To(ContainSubstring("plan (nothing written yet; the question below applies it):\n\nproject "+proj+":\n  install review"), "the plan first")
		untouched()
		pp, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(pp.Store, "review")).To(BeADirectory(), "the source was fetched into the cache before Migrate?, and a No leaves it there")
	})

	It("migrate: start --run (a missing root)--> root", func() {
		fromAnotherMachine()
		p := &scripted{roots: []string{cancelRun}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-root:/Users/alice"}))
		Expect(p.rootSteps[0]).To(Equal(cli.MigrateRootStep{Old: "/Users/alice", Suggest: filepath.Join(tmp, "home"), Value: filepath.Join(tmp, "home"), Records: 1}))
		Expect(read(filepath.Join(moved, ".claude/commands/ship.md"))).To(Equal("python ship\n"))
	})

	It("migrate: start --run (a missing assistant, one to offer)--> targets", func() {
		fakeAssistant(tmp, "opencode", "1.0.0")
		userRecords("claude-code")
		p := &scripted{mapTargets: [][]string{{cancelRun}}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-targets:claude-code"}))
		Expect(p.mapTargetSteps[0]).To(Equal(cli.MigrateTargetStep{Old: "claude-code", Supported: true, Modules: 1, Options: []string{"opencode"}}),
			"every option ticked; a first screen: Esc stays")
		Expect(home).To(BeADirectory())
	})

	It("offers agents for a Cursor record when no assistant is on your PATH", func() {
		userRecords("cursor")
		p := &scripted{mapTargets: [][]string{{cancelRun}}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-targets:cursor"}))
		Expect(p.mapTargetSteps[0]).To(Equal(cli.MigrateTargetStep{Old: "cursor", Supported: false, Modules: 1, Options: []string{"agents"}}))
	})

	It("migrate: start --run (a missing assistant, none to offer)--> confirm", func() {
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n" +
			"- {module: review, assistant: claude-code, scope: project, project_path: " + proj + ", commands: [ship]}\n" +
			"- {module: review, assistant: claude-code, scope: user, commands: [ship]}\n"})
		p := &scripted{confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Migrate?"}), "no assistant to offer: not asked")
		Expect(r.err).To(ContainSubstring("unmappable review (claude-code): claude-code is not installed here"))
		untouched()
	})

	It("migrate: start --run (--map-root or --map-target)--> confirm", func() {
		fromAnotherMachine()
		p := &scripted{confirm: []bool{false}}
		r := lolaP(p, "migrate", "--map-root", "/Users/alice="+filepath.Join(tmp, "home"), "--map-target", "claude-code=opencode")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Migrate?"}), "what the flags map is not asked")
		Expect(p.confirmNavs[0]).To(Equal(cli.Nav{}))
		Expect(r.err).To(ContainSubstring("project " + moved + ":\n  install review (claude-code)"))
	})

	It("migrate: start --run (-n)--> dry run", func() {
		fromAnotherMachine()
		p := &scripted{roots: []string{filepath.Join(tmp, "home")}, mapTargets: [][]string{{"opencode"}}}
		r := lolaP(p, "migrate", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-root:/Users/alice", "migrate-targets:claude-code"}), "the mapping questions are asked, Migrate? is not")
		Expect(r.out).To(ContainSubstring("project " + moved + ":\n  install review (claude-code)"))
		Expect(read(filepath.Join(moved, ".claude/commands/ship.md"))).To(Equal("python ship\n"))
		Expect(home).To(BeADirectory())
		Expect(filepath.Join(tmp, "state", "lola", "trash")).NotTo(BeAnExistingFile())
	})

	It("migrate: start --run (-y)--> done", func() {
		p := &scripted{}
		r := lolaP(p, "migrate", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty())
		Expect(r.out).To(ContainSubstring("✓ migrated project " + proj))
		Expect(read(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal("Ship $ARGUMENTS\n"))
	})

	It("migrate: start --run (--force)--> root", func() {
		fromAnotherMachine()
		p := &scripted{roots: []string{filepath.Join(tmp, "home")}, mapTargets: [][]string{{"opencode"}}}
		r := lolaP(p, "migrate", "--force")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-root:/Users/alice", "migrate-targets:claude-code"}), "--force answers Migrate?, not the mapping questions")
		Expect(r.out).To(ContainSubstring("✓ migrated project " + moved))
	})

	It("migrate: start --run (--json, on a terminal)--> confirm", func() {
		fakeAssistant(tmp, "opencode", "1.0.0")
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n" +
			"- {module: review, assistant: claude-code, scope: project, project_path: " + proj + ", commands: [ship]}\n" +
			"- {module: other, assistant: claude-code, scope: user, commands: [ship]}\n"})
		p := &scripted{confirm: []bool{false}}
		r := lolaP(p, "migrate", "--json")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Migrate?"}), "under --json the mapping questions are not asked")
		Expect(r.out).To(BeEmpty())
		untouched()
	})

	It("migrate: start --run (nothing to migrate)--> done", func() {
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n- {module: review, assistant: claude-code, scope: user}\n"})
		p := &scripted{}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(5), r.err)
		Expect(p.calls).To(BeEmpty())
		Expect(r.out).To(HavePrefix("nothing more to migrate"))
		Expect(home).To(BeADirectory())
	})

	It("migrate: root --empty submit--> next step", func() {
		fromAnotherMachine()
		p := &scripted{roots: []string{""}, mapTargets: [][]string{{"opencode"}}, confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-root:/Users/alice", "migrate-targets:claude-code", "confirm:Migrate?"}))
		Expect(r.err).To(ContainSubstring("unmappable review (claude-code at /Users/alice/proj): project_path /Users/alice/proj no longer exists"), "its records are left")
	})

	It("migrate: root --esc (after another root)--> root", func() {
		bob := filepath.Join(tmp, "bob")
		writeTree(filepath.Join(bob, "proj"), map[string]string{".claude/commands/ship.md": "python ship\n"})
		writeTree(moved, map[string]string{".claude/commands/ship.md": "python ship\n"})
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n" +
			"- {module: review, assistant: claude-code, scope: project, project_path: /Users/alice/proj, commands: [ship]}\n" +
			"- {module: review, assistant: claude-code, scope: project, project_path: /Users/bob/proj, commands: [ship]}\n"})
		p := &scripted{roots: []string{"/srv/a", goBack, filepath.Join(tmp, "home"), bob}, confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-root:/Users/alice", "migrate-root:/Users/bob", "migrate-root:/Users/alice", "migrate-root:/Users/bob", "confirm:Migrate?"}))
		Expect(p.rootSteps[1].Nav).To(Equal(cli.Nav{Back: true, Answered: 1}))
		Expect(p.rootSteps[2].Value).To(Equal("/srv/a"), "asked again as answered")
		Expect(r.err).To(ContainSubstring("project " + moved + ":"))
		Expect(r.err).To(ContainSubstring("project " + filepath.Join(bob, "proj") + ":"))
	})

	It("migrate: root --ctrl+c--> cancelled", func() {
		fromAnotherMachine()
		p := &scripted{roots: []string{cancelRun}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(4), r.err)
		Expect(read(filepath.Join(moved, ".claude/commands/ship.md"))).To(Equal("python ship\n"))
		Expect(home).To(BeADirectory())
		Expect(filepath.Join(tmp, "state", "lola", "trash")).NotTo(BeAnExistingFile())
	})

	It("migrate: targets --empty submit--> next step", func() {
		fakeAssistant(tmp, "opencode", "1.0.0")
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n" +
			"- {module: review, assistant: claude-code, scope: project, project_path: " + proj + ", commands: [ship]}\n" +
			"- {module: review, assistant: claude-code, scope: user, commands: [ship]}\n"})
		p := &scripted{mapTargets: [][]string{{}}, confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-targets:claude-code", "confirm:Migrate?"}))
		Expect(r.err).To(ContainSubstring("unmappable review (claude-code): claude-code is not installed here"), "the records are left")
	})

	It("migrate: targets --esc (after another assistant)--> targets", func() {
		fakeAssistant(tmp, "opencode", "1.0.0")
		userRecords("claude-code", "gemini-cli")
		p := &scripted{mapTargets: [][]string{{"opencode"}, {goBack}, {"opencode"}, {"opencode"}}, confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-targets:claude-code", "migrate-targets:gemini-cli", "migrate-targets:claude-code",
			"migrate-targets:gemini-cli", "confirm:Migrate?"}))
		Expect(p.mapTargetSteps[1].Nav).To(Equal(cli.Nav{Back: true, Answered: 1}))
		Expect(p.mapTargetSteps[2]).To(And(HaveField("Again", true), HaveField("Preselect", []string{"opencode"})), "asked again as answered")
	})

	It("migrate: targets --esc (first screen)--> targets", func() {
		fakeAssistant(tmp, "opencode", "1.0.0")
		userRecords("claude-code")
		p := &scripted{mapTargets: [][]string{{"opencode"}}, confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.mapTargetSteps[0].Nav).To(Equal(cli.Nav{}), "no question before it: Esc stays")
	})

	It("migrate: targets --ctrl+c--> cancelled", func() {
		fakeAssistant(tmp, "opencode", "1.0.0")
		userRecords("claude-code")
		p := &scripted{mapTargets: [][]string{{cancelRun}}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-targets:claude-code"}))
		Expect(home).To(BeADirectory())
		Expect(lola("list", "--user").out).NotTo(ContainSubstring("review"))
	})

	It("migrate: confirm --yes (a hook or script without your consent)--> consent", func() {
		mark := filepath.Join(tmp, "hook-ran")
		writeTree(src, map[string]string{"lola.yml": "name: review\nhooks: {pre-install: pre.sh}\n", "pre.sh": "#!/bin/sh\ntouch '" + mark + "'\n"})
		Expect(os.Chmod(filepath.Join(src, "pre.sh"), 0o755)).To(Succeed())
		p := &scripted{confirm: []bool{true}, consent: []bool{true}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Migrate?", "consent:Run review's install hooks?"}), "the hook question after Migrate?")
		Expect(mark).To(BeAnExistingFile())
		Expect(r.out).To(ContainSubstring("✓ migrated project " + proj))
	})

	It("migrate: consent --no (a pre-install hook)--> failed", func() {
		mark := filepath.Join(tmp, "hook-ran")
		writeTree(src, map[string]string{"lola.yml": "name: review\nhooks: {pre-install: pre.sh}\n", "pre.sh": "#!/bin/sh\ntouch '" + mark + "'\n"})
		Expect(os.Chmod(filepath.Join(src, "pre.sh"), 0o755)).To(Succeed())
		p := &scripted{confirm: []bool{true}, consent: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(4))
		Expect(r.out + r.err).To(ContainSubstring("review has a pre-install hook, and lola runs hooks only with your consent"))
		Expect(mark).NotTo(BeAnExistingFile())
		untouched()
	})

	It("migrate: confirm --yes (--trust)--> receipt", func() {
		writeTree(src, map[string]string{"lola.yml": "name: review\nhooks: {pre-install: pre.sh}\n", "pre.sh": "#!/bin/sh\nexit 0\n"})
		Expect(os.Chmod(filepath.Join(src, "pre.sh"), 0o755)).To(Succeed())
		p := &scripted{confirm: []bool{true, false}}
		r := lolaP(p, "migrate", "--trust=hooks")
		Expect(r.code).To(Equal(4), r.err)
		Expect(r.out+r.err).To(ContainSubstring("    | exit 0\n"), "the preview shows the hook in full, so --trust consents to it")
		Expect(p.calls).To(Equal([]string{"confirm:Migrate?", "confirm:Proceed?"}))
		Expect(r.err).To(ContainSubstring("trusting, as --trust=hooks allows:\n"))
		untouched()
	})

	It("migrate: confirm --yes (a destination fails)--> failed", func() {
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n" +
			"- {module: review, assistant: claude-code, scope: project, project_path: " + proj + ", commands: [ship]}\n" +
			"- {module: review, assistant: claude-code, scope: user, commands: [ship]}\n"})
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		claude := filepath.Join(proj, ".claude", "commands")
		p := &confirming{answers: []bool{true}, meddle: func() { Expect(os.Chmod(claude, 0o500)).To(Succeed()) }}
		DeferCleanup(func() { _ = os.Chmod(claude, 0o755) })
		r := lolaAsking(p, "migrate")
		Expect(r.code).To(Equal(1), r.out)
		Expect(p.questions).To(Equal([]string{"Migrate?"}))
		Expect(r.out).To(ContainSubstring("✓ migrated user scope"), "the other destination is migrated")
		Expect(read(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal("python ship\n"))
	})

	It("migrate: confirm --ctrl+c--> cancelled", func() {
		p := &scripted{confirmCancels: 1}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Migrate?"}))
		untouched()
	})

	It("migrate: confirm --esc (after an assistant question)--> targets", func() {
		fromAnotherMachine()
		p := &scripted{roots: []string{filepath.Join(tmp, "home")}, mapTargets: [][]string{{"opencode"}, {}}, confirmBacks: 1, confirm: []bool{true}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(5), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-root:/Users/alice", "migrate-targets:claude-code", "confirm:Migrate?",
			"migrate-targets:claude-code", "confirm:Migrate?"}))
		Expect(p.confirmNavs[0]).To(Equal(cli.Nav{Back: true, Answered: 2}), "Esc has the mapping questions to go back to")
		Expect(p.mapTargetSteps[1]).To(Equal(cli.MigrateTargetStep{Old: "claude-code", Supported: true, Modules: 1, Options: []string{"opencode"},
			Again: true, Preselect: []string{"opencode"}, Nav: cli.Nav{Back: true, Answered: 1}}), "asked again as answered")
		Expect(r.out).To(ContainSubstring("✓ migrated project " + moved))
		Expect(lola("list", "--user").out).NotTo(ContainSubstring("opencode"), "the second answer, none, stands")
		fetchedOnce()
	})

	It("migrate: confirm --esc (after root questions only)--> root", func() {
		writeTree(moved, map[string]string{".claude/commands/ship.md": "python ship\n"})
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n" +
			"- {module: review, assistant: claude-code, scope: project, project_path: /Users/alice/proj, commands: [ship]}\n"})
		p := &scripted{roots: []string{filepath.Join(tmp, "home"), filepath.Join(tmp, "home")}, confirmBacks: 1, confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-root:/Users/alice", "confirm:Migrate?", "migrate-root:/Users/alice", "confirm:Migrate?"}))
		Expect(p.confirmNavs[0]).To(Equal(cli.Nav{Back: true, Answered: 1}))
		Expect(p.rootSteps[1].Value).To(Equal(filepath.Join(tmp, "home")), "the root is offered as answered")
		Expect(p.rootSteps[1].Nav).To(Equal(cli.Nav{}), "the first question has nothing before it")
		Expect(strings.Count(r.err, "project "+moved+":\n  install review (claude-code)")).To(Equal(2), "the plan is shown again for the answers")
		Expect(read(filepath.Join(moved, ".claude/commands/ship.md"))).To(Equal("python ship\n"), "nothing written")
		Expect(home).To(BeADirectory())
		fetchedOnce()
	})

	It("migrate: confirm --esc (no mapping question)--> confirm", func() {
		p := &scripted{confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Migrate?"}))
		Expect(p.confirmNavs[0]).To(Equal(cli.Nav{}), "a first screen: Esc stays")
		untouched()
	})
})

var _ = Describe("consent paths", func() {
	var tmp, proj, kit, rt, plain, consentJSON string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, kit, rt, plain = filepath.Join(tmp, "proj"), filepath.Join(tmp, "kit"), filepath.Join(tmp, "rt"), filepath.Join(tmp, "plain")
		consentJSON = filepath.Join(tmp, "state", "lola", "consent.json")
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		writeTree(kit, map[string]string{
			"lola.yml":             "name: kit\nhooks: {pre-install: pre.sh, post-install: post.sh}\n",
			"skills/lint/SKILL.md": "---\ndescription: Lint\n---\nLint.\n",
			"pre.sh":               "#!/bin/sh\ntouch '" + filepath.Join(tmp, "pre-ran") + "'\n",
			"post.sh":              "#!/bin/sh\ntouch '" + filepath.Join(tmp, "post-ran") + "'\n",
		})
		Expect(os.Chmod(filepath.Join(kit, "pre.sh"), 0o755)).To(Succeed())
		Expect(os.Chmod(filepath.Join(kit, "post.sh"), 0o755)).To(Succeed())
		writeTree(rt, map[string]string{
			".claude-plugin/plugin.json": `{"name":"rt"}`,
			"skills/tidy/SKILL.md":       "---\ndescription: Tidy\n---\nTidy.\n",
			"hooks/hooks.json":           `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/stop.sh"}]}]}}`,
			"stop.sh":                    "#!/bin/sh\nexit 0\n",
		})
		writeTree(plain, map[string]string{"commands/ship.md": "Ship $ARGUMENTS\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
	})

	// scripts gives the project a pre-install and a post-install script, each touching a file
	// named after its event when it runs.
	scripts := func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\npre_install: ci/check.sh\npost_install: ci/after.sh\n",
			"ci/check.sh": "#!/bin/sh\ntouch '" + filepath.Join(tmp, "check-ran") + "'\n",
			"ci/after.sh": "#!/bin/sh\ntouch '" + filepath.Join(tmp, "after-ran") + "'\n"})
		Expect(os.Chmod(filepath.Join(proj, "ci", "check.sh"), 0o755)).To(Succeed())
		Expect(os.Chmod(filepath.Join(proj, "ci", "after.sh"), 0o755)).To(Succeed())
	}
	ran := func(name string) bool {
		_, err := os.Stat(filepath.Join(tmp, name))
		return err == nil
	}
	lock := func() string {
		GinkgoHelper()
		return read(filepath.Join(proj, ".lola", "lock.json"))
	}
	// untouched asserts the run wrote nothing: no files, no lock, no consent, no hook run.
	untouched := func() {
		GinkgoHelper()
		Expect(filepath.Join(proj, ".claude", "skills")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".claude", "commands")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".lola", "lock.json")).NotTo(BeAnExistingFile())
		Expect(consentJSON).NotTo(BeAnExistingFile())
		Expect(ran("pre-ran") || ran("post-ran") || ran("check-ran") || ran("after-ran")).To(BeFalse(), "nothing ran")
	}
	install := func(p *scripted, mod string, more ...string) result {
		return lolaP(p, append([]string{"install", mod, "-C", proj, "-t", "claude-code"}, more...)...)
	}

	It("consent: start --run (hooks without your consent, on a terminal)--> hooks", func() {
		p := &scripted{consentCancels: 1}
		r := install(p, kit, "-y")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Run kit's install hooks?"}))
		Expect(r.err).To(MatchRegexp(`kit has install hooks\. They run as you, unconfined:\n    pre-install: pre\.sh \(sha256 [0-9a-f]{64}\)\n`+
			`    \| #!/bin/sh\n    \| touch '[^']*/pre-ran'\n`+
			`    post-install: post\.sh \(sha256 [0-9a-f]{64}\)\n    \| #!/bin/sh\n    \| touch '[^']*/post-ran'\n`+
			`  Consent covers only the scripts listed, not other files they run; review those too\.\n`), "each hook in full")
		untouched()
	})

	It("consent: start --run (Claude Code hooks without your consent, on a terminal)--> runtime", func() {
		p := &scripted{consentCancels: 1}
		r := install(p, rt, "-y")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Install rt's Claude Code hooks?"}))
		Expect(r.err).To(ContainSubstring("rt has Claude Code hooks:\n    Stop: command\n      runs: "))
		Expect(r.err).To(ContainSubstring("\n  These run inside Claude Code with your permissions.\n"))
		untouched()
	})

	It("consent: start --run (a project script without your consent, on a terminal)--> script", func() {
		scripts()
		p := &scripted{consentCancels: 1}
		r := install(p, plain, "-y")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Run this project's pre-install script?"}))
		Expect(r.err).To(MatchRegexp(`The project at `+regexp.QuoteMeta(proj)+` has a pre-install script\. It runs as you, unconfined, for every module lola installs there:\n`+
			`    ci/check\.sh \(sha256 [0-9a-f]{64}\)\n    \| #!/bin/sh\n    \| touch '[^']*/check-ran'\n`), "the script in full")
		untouched()
	})

	It("consent: start --run (lola sync, nothing to install)--> later", func() {
		scripts()
		Expect(lola("install", plain, "-C", proj, "-t", "claude-code", "-y", "--trust=scripts", "--unseen").code).To(Equal(0))
		Expect(os.Remove(filepath.Join(tmp, "check-ran"))).To(Succeed())
		writeTree(proj, map[string]string{"ci/check.sh": "#!/bin/sh\ntouch '" + filepath.Join(tmp, "check-ran") + "'\nexit 0\n"})
		before := read(consentJSON)
		p := &scripted{consentCancels: 1}
		r := lolaP(p, "sync", "-C", proj)
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Trust this project's pre-install script for later runs?"}))
		Expect(read(consentJSON)).To(Equal(before))
		Expect(ran("check-ran")).To(BeFalse())
	})

	It("consent: start --run (consent recorded)--> done", func() {
		Expect(install(&scripted{consent: []bool{true}}, kit, "-y").code).To(Equal(0))
		Expect(os.RemoveAll(filepath.Join(proj, ".claude", "skills"))).To(Succeed())
		p := &scripted{}
		r := lolaP(p, "sync", "-C", proj, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "the consent recorded for the hooks' digest answers")
		Expect(filepath.Join(proj, ".claude", "skills", "lint", "SKILL.md")).To(BeARegularFile())
	})

	It("consent: start --run (-y or --force)--> hooks", func() {
		for _, flag := range []string{"-y", "--force"} {
			p := &scripted{consentCancels: 1}
			r := install(p, kit, flag)
			Expect(r.code).To(Equal(4), r.err)
			Expect(p.calls).To(Equal([]string{"consent:Run kit's install hooks?"}), flag+" never answers consent")
			untouched()
		}
	})

	It("consent: start --run (--trust)--> receipt", func() {
		p := &scripted{confirm: []bool{true}}
		r := install(p, kit, "--trust=hooks", "--unseen")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Proceed?"}), "the kind --trust covers is not asked")
		Expect(r.err).To(ContainSubstring("trusting, as --trust=hooks allows:\n  → hooks: kit pre-install (new), kit post-install (new)\n"))
		Expect(ran("pre-ran") && ran("post-ran")).To(BeTrue())
		Expect(read(consentJSON)).To(ContainSubstring(`"kit"`))
	})

	It("consent: start --run (-n)--> done", func() {
		scripts()
		p := &scripted{}
		r := install(p, kit, "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "a dry run asks nothing")
		Expect(r.out).To(ContainSubstring("pre-install hook pre.sh"))
		untouched()
	})

	It("consent: start --run (--json or no terminal, a pre-install hook or script)--> refused", func() {
		for _, args := range [][]string{{"-y", "--json"}, {"-y"}} {
			p := &scripted{}
			var r result
			if len(args) == 2 {
				r = install(p, kit, args...)
			} else {
				r = lola(append([]string{"install", kit, "-C", proj, "-t", "claude-code"}, args...)...)
			}
			Expect(r.code).To(Equal(3), r.err)
			Expect(p.calls).To(BeEmpty())
			Expect(r.err).To(ContainSubstring("kit has a pre-install hook, and lola runs hooks only with your consent"))
			Expect(r.err).To(ContainSubstring("--trust=hooks"))
			untouched()
		}
	})

	It("consent: hooks --yes--> next step", func() {
		scripts()
		p := &scripted{consent: []bool{true, true, true}}
		r := install(p, kit, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Run kit's install hooks?", "consent:Run this project's pre-install script?",
			"consent:Run this project's post-install script?"}))
		Expect(ran("pre-ran") && ran("post-ran") && ran("check-ran") && ran("after-ran")).To(BeTrue())
		Expect(read(consentJSON)).To(ContainSubstring(`"kit"`))
		Expect(lock()).NotTo(ContainSubstring("pending"))
	})

	It("consent: hooks --no (a pre-install hook)--> refused", func() {
		p := &scripted{consent: []bool{false}}
		r := install(p, kit, "-y")
		Expect(r.code).To(Equal(4))
		Expect(r.err).To(ContainSubstring("error: kit has a pre-install hook, and lola runs hooks only with your consent: pre-install: pre.sh (hook sha256 "))
		Expect(r.err).To(ContainSubstring("  hint: then consent to it: lola install " + kit))
		untouched()
	})

	It("consent: hooks --no (post-install hooks only)--> done", func() {
		writeTree(kit, map[string]string{"lola.yml": "name: kit\nhooks: {post-install: post.sh}\n"})
		p := &scripted{consent: []bool{false}}
		r := install(p, kit, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Run kit's install hooks?"}))
		Expect(filepath.Join(proj, ".claude", "skills", "lint", "SKILL.md")).To(BeARegularFile())
		Expect(ran("post-ran")).To(BeFalse())
		Expect(lock()).To(ContainSubstring(`"hooks_pending": true`))
		Expect(consentJSON).NotTo(BeAnExistingFile())
		Expect(r.out + r.err).To(ContainSubstring("kit: post-install hook skipped without your consent; review and run it: lola sync -C " + proj + " kit"))
	})

	It("consent: hooks --ctrl+c--> cancelled", func() {
		scripts()
		p := &scripted{consentCancels: 1}
		r := install(p, kit, "-y")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Run kit's install hooks?"}))
		untouched()
	})

	It("consent: runtime --yes--> next step", func() {
		p := &scripted{consent: []bool{true}}
		r := install(p, rt, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Install rt's Claude Code hooks?"}))
		Expect(r.out).To(ContainSubstring("rt: 1 runtime hook for claude-code in .claude/settings.local.json (granted)"))
		Expect(consentJSON).To(BeARegularFile())
		p = &scripted{}
		Expect(lolaP(p, "sync", "-C", proj, "-y").code).To(Equal(0))
		Expect(p.calls).To(BeEmpty(), "not asked again")
	})

	It("consent: runtime --no--> next step", func() {
		p := &scripted{consent: []bool{false}}
		r := install(p, rt, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(proj, ".claude", "skills", "tidy", "SKILL.md")).To(BeARegularFile(), "the rest is installed")
		Expect(r.out).NotTo(ContainSubstring("(consented)"))
		Expect(consentJSON).NotTo(BeAnExistingFile())
		p = &scripted{}
		r = lolaP(p, "sync", "-C", proj, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "declined earlier: the next sync does not ask again")
		Expect(r.out).To(ContainSubstring("▸ 1 runtime hook declined earlier"))
	})

	It("consent: runtime --ctrl+c--> cancelled", func() {
		p := &scripted{consentCancels: 1}
		r := install(p, rt, "-y")
		Expect(r.code).To(Equal(4), r.err)
		untouched()
	})

	It("consent: script --yes--> next step", func() {
		scripts()
		p := &scripted{consent: []bool{true, true}}
		r := install(p, plain, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Run this project's pre-install script?", "consent:Run this project's post-install script?"}))
		Expect(ran("check-ran") && ran("after-ran")).To(BeTrue())
		Expect(consentJSON).To(BeARegularFile())
	})

	It("consent: script --no (pre-install)--> refused", func() {
		scripts()
		p := &scripted{consent: []bool{false}}
		r := install(p, plain, "-y")
		Expect(r.code).To(Equal(4))
		Expect(p.calls).To(Equal([]string{"consent:Run this project's pre-install script?"}))
		Expect(r.err).To(ContainSubstring("pre-install script ci/check.sh"))
		untouched()
	})

	It("consent: script --no (post-install)--> done", func() {
		scripts()
		p := &scripted{consent: []bool{true, false}}
		r := install(p, plain, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(ran("check-ran")).To(BeTrue())
		Expect(ran("after-ran")).To(BeFalse())
		Expect(lock()).To(ContainSubstring(`"scripts_pending": true`))
		Expect(r.out + r.err).To(ContainSubstring("post-install script ci/after.sh (project) skipped without your trust; review and run it: lola sync -C " + proj + " plain"))
	})

	It("consent: script --ctrl+c--> cancelled", func() {
		scripts()
		p := &scripted{consentCancels: 1}
		r := install(p, plain, "-y")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Run this project's pre-install script?"}))
		untouched()
	})

	It("consent: later --yes--> done", func() {
		scripts()
		Expect(lola("install", plain, "-C", proj, "-t", "claude-code", "-y", "--trust=scripts", "--unseen").code).To(Equal(0))
		Expect(os.Remove(filepath.Join(tmp, "check-ran"))).To(Succeed())
		writeTree(proj, map[string]string{"ci/check.sh": "#!/bin/sh\ntouch '" + filepath.Join(tmp, "check-ran") + "'\nexit 0\n"})
		before := read(consentJSON)
		p := &scripted{consent: []bool{true}}
		r := lolaP(p, "sync", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(consentJSON)).NotTo(Equal(before), "the new digest is recorded")
		Expect(ran("check-ran")).To(BeFalse(), "nothing runs: there is nothing to wrap")
	})

	It("consent: later --no--> refused", func() {
		scripts()
		Expect(lola("install", plain, "-C", proj, "-t", "claude-code", "-y", "--trust=scripts", "--unseen").code).To(Equal(0))
		writeTree(proj, map[string]string{"ci/check.sh": "#!/bin/sh\nexit 0\n"})
		before := read(consentJSON)
		p := &scripted{consent: []bool{false}}
		r := lolaP(p, "sync", "-C", proj)
		Expect(r.code).To(Equal(4))
		Expect(r.err).To(ContainSubstring("pre-install script ci/check.sh"))
		Expect(read(consentJSON)).To(Equal(before))
	})

	It("consent: a dry run of a sync with nothing to install says the script is not trusted yet, and the command that trusts it works", func() {
		scripts()
		Expect(lola("install", plain, "-C", proj, "-t", "claude-code", "-y", "--trust=scripts", "--unseen").code).To(Equal(0))
		writeTree(proj, map[string]string{"ci/check.sh": "#!/bin/sh\nexit 0\n"})
		r := lola("sync", "-n", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		trust := "lola sync -C " + proj + " --trust=scripts"
		Expect(r.out).To(ContainSubstring("not trusted yet; to trust it: " + trust + "\n"))
		Expect(r.out).To(MatchRegexp(`post-install script ci/after.sh \(project at [^)]*\): trusted; nothing to wrap now\n`))
		Expect(r.out).NotTo(ContainSubstring("trusted for later"))
		plainRows := lola("sync", "-n", "-C", proj, "--plain").out
		Expect(plainRows).To(MatchRegexp(`(?m)^script\t\tpre-install\tproject\tci/check.sh\t\tlater\t[0-9a-f]{64}\tfalse\t` + regexp.QuoteMeta(trust+" --plain") + `$`))
		Expect(plainRows).To(MatchRegexp(`(?m)^script\t\tpost-install\tproject\tci/after.sh\t\tlater\t[0-9a-f]{64}\ttrue\t-$`))
		var doc struct {
			Scripts []map[string]any `json:"scripts"`
		}
		Expect(json.Unmarshal([]byte(lola("sync", "-n", "-C", proj, "--json").out), &doc)).To(Succeed())
		Expect(doc.Scripts).To(ConsistOf(
			And(HaveKeyWithValue("path", "ci/check.sh"), HaveKeyWithValue("status", "later"), HaveKeyWithValue("trusted", false), HaveKeyWithValue("trust_run", trust+" --json")),
			And(HaveKeyWithValue("path", "ci/after.sh"), HaveKeyWithValue("status", "later"), HaveKeyWithValue("trusted", true), HaveKeyWithValue("trust_run", ""))))
		before := read(consentJSON)
		Expect(runHint(trust).code).To(Equal(0))
		Expect(read(consentJSON)).NotTo(Equal(before), "the printed command trusts it")
		r = lola("sync", "-n", "-C", proj)
		Expect(r.out).NotTo(ContainSubstring("not trusted yet"), "trusted now, so it is not listed")
	})
})

var _ = Describe("init paths", func() {
	var tmp, dir string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		dir = filepath.Join(tmp, "fresh")
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		GinkgoT().Chdir(dir)
	})
	question := func() string { return "confirm:Create module fresh in " + dir + "?" }
	// empty asserts nothing was scaffolded in d.
	empty := func(d string) {
		GinkgoHelper()
		entries, err := os.ReadDir(d)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(BeEmpty(), "nothing written")
	}

	It("init: start --run--> confirm", func() {
		p := &scripted{confirm: []bool{false}}
		r := lolaP(p, "init")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{question()}))
		Expect(r.err).To(HavePrefix("will create module fresh in "+dir+" (agent-plugins):\n    plugin.json\n"), "the plan first")
		empty(dir)
	})

	It("init: start --dir--> done", func() {
		p := &scripted{}
		r := lolaP(p, "init", filepath.Join(tmp, "other"))
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "a directory given asks nothing, on a terminal too")
		Expect(filepath.Join(tmp, "other", "plugin.json")).To(BeARegularFile())
	})

	It("init: start --run (-y)--> done", func() {
		p := &scripted{}
		r := lolaP(p, "init", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty())
		Expect(filepath.Join(dir, "plugin.json")).To(BeARegularFile())
	})

	It("init: start --run (--json, on a terminal)--> confirm", func() {
		p := &scripted{confirm: []bool{true}}
		r := lolaP(p, "init", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{question()}))
		Expect(r.out).To(ContainSubstring(`"files": [`), "the result as one JSON document on stdout")
		Expect(filepath.Join(dir, "plugin.json")).To(BeARegularFile())
	})

	It("init: confirm --yes (a write fails)--> failed", func() {
		if os.Geteuid() == 0 {
			Skip("root writes into a read-only directory")
		}
		locked := filepath.Join(dir, "dev.getlola")
		p := &confirming{answers: []bool{true}, meddle: func() { Expect(os.Mkdir(locked, 0o500)).To(Succeed()) }}
		DeferCleanup(os.Chmod, locked, os.FileMode(0o755))
		r := lolaAsking(p, "init")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("permission denied"))
		Expect(filepath.Join(dir, "plugin.json")).To(BeARegularFile(), "what was written before it stays: a partial scaffold")
		Expect(filepath.Join(dir, "skills", "hello", "SKILL.md")).To(BeARegularFile())
		Expect(filepath.Join(locked, "commands")).NotTo(BeAnExistingFile())
	})

	It("init: confirm --esc--> confirm", func() {
		p := &scripted{confirm: []bool{false}}
		Expect(lolaP(p, "init").code).To(Equal(4))
		Expect(p.confirmNavs).To(Equal([]cli.Nav{{}}), "a first screen: Esc stays")
		empty(dir)
	})

	It("init: confirm --ctrl+c--> cancelled", func() {
		p := &scripted{confirmCancels: 1}
		r := lolaP(p, "init")
		Expect(r.code).To(Equal(4), r.err)
		Expect(p.calls).To(Equal([]string{question()}))
		empty(dir)
	})
})
