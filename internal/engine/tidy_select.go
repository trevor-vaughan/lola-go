package engine

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// tidySelect decides what moves (§3.1, §6.3): what the names or --all select, else what Choose
// answers; then the review, also for a Choose answer that only keeps. It returns nil when
// nothing moves, and refuses with the result's candidates listed when it needs an answer it
// cannot ask for.
func (e *Engine) tidySelect(req TidyRequest, s *tidyScanner, scan TidyScan, res *TidyResult) ([]TidyUnit, TidyChoice, error) {
	refuse := func(listed []TidyUnit, err error) ([]TidyUnit, TidyChoice, error) {
		res.Candidates = listed
		return nil, TidyChoice{}, &TidyError{Result: res, err: err}
	}
	var chosen []TidyUnit
	var choice TidyChoice
	switch {
	case len(req.Select) > 0:
		for _, sel := range req.Select {
			n := 0
			for _, c := range scan.Candidates {
				if sel.Matches(c.Kind, c.Name) {
					n++
					if !slices.ContainsFunc(chosen, func(u TidyUnit) bool { return u.ID == c.ID }) {
						chosen = append(chosen, c)
					}
				}
			}
			if n == 0 {
				return nil, TidyChoice{}, e.tidyUnmatched(req, s, sel)
			}
		}
		sortTidy(chosen)
	case req.All:
		chosen = slices.Clone(scan.Candidates)
	case len(scan.Candidates) == 0:
		return nil, TidyChoice{}, nil // an empty scan (§9)
	case req.Dry:
		chosen = slices.Clone(scan.Candidates)
	case len(req.Keep) > 0 || len(req.Unkeep) > 0:
		return nil, TidyChoice{}, nil // rule 4
	case req.Force:
		r := req
		return refuse(scan.Candidates, errs.WithHints(fmt.Errorf("--force needs names or --all: %s can move", CountItems(len(scan.Candidates))),
			tidyHints(e.tidyRun(r, nil, "--all"))...))
	case req.Choose == nil:
		r := req
		r.Yes = true
		return refuse(scan.Candidates, errs.WithHints(fmt.Errorf("lola tidy needs names or --all when it cannot ask (off a terminal or with --json): %s can move",
			CountItems(len(scan.Candidates))), tidyHints(e.tidyRun(r, nil, "--all"))...))
	default:
		var err error
		if choice, err = req.Choose(scan); err != nil {
			return nil, TidyChoice{}, err
		}
		if chosen, err = offered(scan, choice); err != nil {
			return nil, TidyChoice{}, err
		}
		if len(chosen) > 0 || len(choice.Keep) > 0 {
			choice.Command, choice.Hidden = e.tidyEquivalent(req, scan, chosen, choice.Keep)
		}
	}
	if len(chosen) == 0 && len(choice.Keep) == 0 {
		return nil, choice, nil
	}
	choice.Items = chosen
	choice.Move = make([]UnitID, len(chosen))
	for i, c := range chosen {
		choice.Move[i] = c.ID
	}
	if req.Dry {
		apply := req
		apply.Dry = false
		if !apply.Force && !req.Interactive {
			apply.Yes = true
		}
		switch {
		case !hasSelection(req) && (len(req.Keep) > 0 || len(req.Unkeep) > 0):
			apply.Yes = false // the run without -n edits the keep-list and moves nothing
		case len(apply.Select) == 0 && (!req.Interactive || apply.Force):
			apply.All = true // on a terminal the run without names runs the picker
		}
		res.Hints = append(res.Hints, tidyHints(e.tidyRerun(apply))...)
		return chosen, choice, nil
	}
	if req.Force || req.Yes {
		return chosen, choice, nil
	}
	if req.Confirm == nil {
		r := req
		r.Yes = true
		names := make([]string, len(chosen))
		for i, c := range chosen {
			names[i] = c.Kind + "/" + c.Name + " (" + c.Rel + ")"
		}
		return refuse(chosen, errs.WithHints(fmt.Errorf("lola tidy would move %s: %s; it cannot ask (off a terminal or with --json), so it needs -y to move them",
			CountItems(len(chosen)), strings.Join(names, ", ")), tidyHints(e.tidyRerun(r))...))
	}
	var seen []config.Pattern
	for _, k := range append(slices.Clone(req.Keep), choice.Keep...) {
		if !slices.Contains(s.keepHeld, k) && !slices.Contains(seen, k) {
			seen = append(seen, k)
			choice.KeepAdd = append(choice.KeepAdd, untrusted.Text(k.String(), untrusted.MaxName))
		}
	}
	seen = nil
	for _, k := range req.Unkeep {
		if !slices.Contains(seen, k) {
			seen = append(seen, k)
			choice.KeepRemove = append(choice.KeepRemove, untrusted.Text(k.String(), untrusted.MaxName))
		}
	}
	ok, err := req.Confirm(choice)
	if err != nil {
		return nil, TidyChoice{}, err
	}
	if !ok {
		return nil, TidyChoice{}, ErrDeclined
	}
	return chosen, choice, nil
}

// CountItems counts n items in words: "1 item", "2 items".
func CountItems(n int) string {
	if n == 1 {
		return "1 item"
	}
	return fmt.Sprintf("%d items", n)
}

// offered is the candidates choice moves, refusing a choice that names a unit the scan did not
// offer, or keeps a selector that is not a candidate's escaped literal selector (R-6d-N3).
func offered(scan TidyScan, choice TidyChoice) ([]TidyUnit, error) {
	var out []TidyUnit
	for _, id := range choice.Move {
		i := slices.IndexFunc(scan.Candidates, func(c TidyUnit) bool { return c.ID == id })
		if i < 0 {
			return nil, errUnoffered
		}
		if !slices.ContainsFunc(out, func(u TidyUnit) bool { return u.ID == id }) {
			out = append(out, scan.Candidates[i])
		}
	}
	for _, k := range choice.Keep {
		if !slices.ContainsFunc(scan.Candidates, func(c TidyUnit) bool { return c.Literal() == k }) {
			return nil, errUnoffered
		}
	}
	sortTidy(out)
	return out, nil
}

// Literal is the selector lola writes for u: its kind and its name with * ? [ \ escaped, so it
// matches u's name and no other (§6.3).
func (u TidyUnit) Literal() config.Pattern {
	var b strings.Builder
	for _, r := range u.Name {
		if strings.ContainsRune(`*?[\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return config.Pattern{Kind: u.Kind, Name: b.String()}
}

// tidyEquivalent is the lola tidy command that selects exactly chosen and keeps keep, evaluated
// against the scan: chosen's literal selectors with the run's flags and keep as --keep, -t
// narrowed to chosen's targets when that is needed; "" when no such command selects exactly
// chosen, or when it would name a selector both to move and to keep, which a run refuses
// (§3.1 rule 3, §6.3). hidden reports a command that exists but that untrusted.Text would alter,
// which tidyCommand returns as "".
func (e *Engine) tidyEquivalent(req TidyRequest, scan TidyScan, chosen []TidyUnit, keep []config.Pattern) (cmd string, hidden bool) {
	var sels []config.Pattern
	var targets []string
	for _, c := range chosen {
		if l := c.Literal(); !slices.Contains(sels, l) {
			sels = append(sels, l)
		}
		if !slices.Contains(targets, c.Target) {
			targets = append(targets, c.Target)
		}
	}
	selects := func(targets []string) bool {
		n := 0
		for _, c := range scan.Candidates {
			if targets != nil && !slices.Contains(targets, c.Target) {
				continue
			}
			if !slices.ContainsFunc(sels, func(p config.Pattern) bool { return p.Matches(c.Kind, c.Name) }) {
				continue
			}
			if !slices.ContainsFunc(chosen, func(u TidyUnit) bool { return u.ID == c.ID }) {
				return false
			}
			n++
		}
		return n == len(chosen)
	}
	r := req
	r.Select, r.All, r.Yes, r.Force = sels, false, false, false
	for _, k := range keep {
		if slices.Contains(sels, k) {
			return "", false
		}
		if !slices.Contains(r.Keep, k) {
			r.Keep = append(slices.Clone(r.Keep), k)
		}
	}
	switch {
	case selects(nil):
	case selects(targets):
		r.Targets = targets
	default:
		return "", false
	}
	cmd = e.tidyRerun(r)
	return cmd, cmd == ""
}

// tidyUnmatched refuses sel, a name that matches no candidate, saying exactly why for each unit
// it does match (§11.3), with a hint for each reason a run can lift.
func (e *Engine) tidyUnmatched(req TidyRequest, s *tidyScanner, sel config.Pattern) error {
	var why, hints []string
	hint := func(h string) {
		if !slices.Contains(hints, h) {
			hints = append(hints, h)
		}
	}
	for _, o := range s.owned {
		if sel.Matches(o.Kind, o.Name) {
			why = append(why, fmt.Sprintf("%s/%s is installed by module %s; lola tidy only moves what lola does not manage", o.Kind, o.Name,
				untrusted.Text(o.owner, untrusted.MaxName)))
			hint(e.tidyCommand(o.ownerScope, "uninstall", append([]string{o.owner}, req.Display...)...))
		}
	}
	for _, p := range s.protected() {
		if !sel.Matches(p.Kind, p.Name) {
			continue
		}
		name := p.Kind + "/" + p.Name
		switch p.Code {
		case codeTracked:
			if p.gitCause != "" {
				why = append(why, fmt.Sprintf("git could not say whether %s is tracked: %s", name, p.gitCause))
			} else {
				why = append(why, name+" is tracked by git")
			}
			r := req
			r.IncludeTracked = true
			hint(e.tidyRerun(r))
		case codeKept:
			why = append(why, e.keptWhy(req, s, p))
			r := req
			if slices.Contains(req.Keep, p.keptBy) && !slices.Contains(s.keepHeld, p.keptBy) {
				r.Keep = slices.DeleteFunc(slices.Clone(req.Keep), func(k config.Pattern) bool { return k == p.keptBy })
			} else {
				r.Unkeep = append(slices.Clone(req.Unkeep), p.keptBy)
			}
			hint(e.tidyRerun(r))
		default:
			why = append(why, name+" "+p.Reason+"; lola never moves it")
		}
	}
	if len(why) == 0 {
		r := req
		r.Select, r.All, r.Yes, r.Force = nil, false, false, false
		return errs.WithHints(fmt.Errorf("%s matches nothing lola can tidy at %s (%s)", untrusted.Text(sel.String(), untrusted.MaxName),
			tidyWhere(req.Scope), strings.Join(s.targetIDs, ", ")), tidyHints(e.tidyRun(r, nil, "-n"))...)
	}
	return errs.WithHints(errors.New(strings.Join(why, "; ")), tidyHints(hints...)...)
}

// keptWhy says which keep entry keeps p and what else it keeps (R-6d-S7).
func (e *Engine) keptWhy(req TidyRequest, s *tidyScanner, p *tidyItem) string {
	from := "keep.yaml, " + tidyWhere(req.Scope)
	if !slices.Contains(s.keepHeld, p.keptBy) {
		from = "--keep"
	}
	msg := fmt.Sprintf("%s/%s is kept by your keep-list entry %s (%s)", p.Kind, p.Name, untrusted.Text(p.keptBy.String(), untrusted.MaxName), from)
	var others []string
	for _, o := range s.protected() {
		if o != p && o.Code == codeKept && o.keptBy == p.keptBy {
			others = append(others, o.Kind+"/"+o.Name)
		}
	}
	if len(others) > 0 {
		msg += "; removing it also stops keeping: " + strings.Join(others, ", ")
	}
	return msg
}
