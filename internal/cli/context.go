package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// printContext reads each scope's context and prints it (addContext); the
// contexts go into the run's --json document (emit) and its refusal (contextError). writes is what
// a command that edits the scopes' files (install, uninstall) was told about where its edit goes,
// nil for any other command (engine.Engine.Context).
func (a *app) printContext(cmd *cobra.Command, eng *engine.Engine, scopes []engine.Scope, writes *engine.TargetRequest) {
	a.contexts = make([]engine.ScopeContext, 0, len(scopes))
	for _, sc := range scopes {
		a.addContext(cmd, eng, sc, writes)
	}
}

// addContext reads sc's context, adds it to the run's and prints it on stderr: the line in human
// output, a context row under --plain (contextRow), nothing under --json, whose document carries it.
func (a *app) addContext(cmd *cobra.Command, eng *engine.Engine, sc engine.Scope, writes *engine.TargetRequest) {
	c := eng.Context(sc, writes)
	a.contexts = append(a.contexts, c)
	switch {
	case a.json:
	case a.plain:
		fmt.Fprintln(cmd.ErrOrStderr(), contextRow(c))
	default:
		s := style{color: a.color(cmd.ErrOrStderr())}
		fmt.Fprintln(cmd.ErrOrStderr(), s.paint("36", contextLine(c)))
	}
}

// contextRow is a scope's context as --plain prints it: context, the scope,
// its root ("" at user scope), its base, the profile on by qualified name, - for none or unknown when lola could not read
// the scope's state (contextFacts's "profile unknown"), and a bundle's members, comma-separated. It goes to stderr with the human line, so stdout's rows keep their
// shape and a module named context is never read as one (plan 12.5 ambiguity 2).
func contextRow(c engine.ScopeContext) string {
	profile, members := "-", []string{}
	if c.Profile == nil && c.Error != "" {
		profile = "unknown"
	}
	if c.Profile != nil {
		profile = c.Profile.Name
		for _, m := range c.Profile.Members {
			members = append(members, m.Name)
		}
	}
	return strings.Join([]string{"context", c.Scope, c.Root, c.Base.Name, profile, strings.Join(members, ",")}, "\t")
}

// contextLine is a scope's context line: "user scope: default", "project <root>:
// profile go over project.yml".
func contextLine(c engine.ScopeContext) string {
	where := "user scope"
	if c.Scope == target.Project.String() {
		where = "project " + c.Root
	}
	return where + ": " + contextFacts(c)
}

// contextFacts is what a context line says after its scope, as the scope picker's rows show it:
// the base, or the profile on over it, with how to restore it when this machine lacks it, or why
// lola could not read it.
func contextFacts(c engine.ScopeContext) string {
	if c.Profile == nil {
		if c.Error != "" {
			return c.Base.Label + " (profile unknown)"
		}
		return c.Base.Label
	}
	p := "profile " + c.Profile.Label
	if c.Profile.Bundle {
		p = "bundle " + c.Profile.Label + " (" + memberList(c.Profile.Members) + ")"
	}
	if c.Restore != "" {
		absent := "" // a bundle's absent members; for a remote profile on, the profile itself
		if c.Profile.Bundle {
			var left []string
			for _, m := range c.Profile.Members {
				if m.Left {
					left = append(left, m.Label)
				}
			}
			absent = strings.Join(left, ", ") + " "
		}
		p += " (" + absent + "not on this machine yet → " + c.Restore + ")"
	}
	if c.Error != "" {
		p += " (unreadable: " + c.Error + ")"
	}
	return p + " over " + c.Base.Label
}

// memberList is a bundle's members as its context line names them: in
// order, the members a nested bundle brings grouped after its name (go-all › go, go-lint), groups
// then separated by "; ", cut after six names with "and N more".
func memberList(ms []engine.ContextLayer) string {
	const most = 6
	more := 0
	if len(ms) > most {
		ms, more = ms[:most], len(ms)-most
	}
	var groups []string
	nested := false
	for i := 0; i < len(ms); {
		j, names := i, []string{}
		for ; j < len(ms) && ms[j].Via == ms[i].Via; j++ {
			names = append(names, ms[j].Label)
		}
		group := strings.Join(names, ", ")
		if ms[i].Via != "" {
			group, nested = ms[i].Via+" › "+group, true
		}
		groups, i = append(groups, group), j
	}
	sep := ", "
	if nested {
		sep = "; "
	}
	if more > 0 {
		groups = append(groups, fmt.Sprintf("and %d more", more))
	}
	return strings.Join(groups, sep)
}

// choiceScopes is the scopes of resolved choices, in order.
func choiceScopes(cs []engine.ScopeChoice) []engine.Scope {
	out := make([]engine.Scope, len(cs))
	for i, c := range cs {
		out[i] = c.Scope
	}
	return out
}

// withContext puts the run's contexts first in doc, a JSON object; doc
// is returned as it is when the run read none, or when it is not an object.
func withContext(doc []byte, contexts []engine.ScopeContext) ([]byte, error) {
	if contexts == nil || len(doc) < 2 || doc[0] != '{' {
		return doc, nil
	}
	c, err := json.Marshal(contexts)
	if err != nil {
		return nil, err
	}
	out := append([]byte(`{"context":`), c...)
	if len(bytes.TrimSpace(doc[1:])) > 1 { // more than the closing brace
		out = append(out, ',')
	}
	return append(out, doc[1:]...), nil
}

// contextError is a refusal after the scopes were resolved: its --json line carries the run's
// contexts beside error, hints and every other field the error carries.
type contextError struct {
	error
	contexts []engine.ScopeContext
}

func (e *contextError) Unwrap() error { return e.error }

// JSONFields carries the contexts beside the fields of the error's own document, if any.
func (e *contextError) JSONFields() map[string]any {
	fields := map[string]any{}
	if f, ok := errors.AsType[jsonFielder](e.error); ok {
		maps.Copy(fields, f.JSONFields())
	}
	fields["context"] = e.contexts
	return fields
}

// carryContext makes the refusal of cmd and of every command under it carry the contexts the run
// printed (contextError).
func (a *app) carryContext(cmd *cobra.Command) {
	if run := cmd.RunE; run != nil {
		cmd.RunE = func(c *cobra.Command, args []string) error {
			err := run(c, args)
			if err != nil && a.contexts != nil {
				return &contextError{error: err, contexts: a.contexts}
			}
			return err
		}
	}
	for _, sub := range cmd.Commands() {
		a.carryContext(sub)
	}
}
