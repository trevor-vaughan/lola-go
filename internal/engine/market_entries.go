package engine

// PickerEntry is an entry a marketplace offers, with whether a scope has it installed and the
// web page of its source: an http(s) address, "" when it has none (a local source, or an ssh remote
// of a host without a known web address).
type PickerEntry struct {
	MarketOffer
	Installed bool
	At        string
	WebURL    string
}

// PickerSkips is each fetched marketplace's skipped entries with why, keyed by its name, as lola
// market <name> lists them; it never fetches. A marketplace with none has no key; warnings are
// left to PickerEntries, which reads the same groups.
func (e *Engine) PickerSkips(display []string) (map[string][]MarketSkip, error) {
	groups, _, err := e.MarketGroups(display)
	if err != nil {
		return nil, err
	}
	out := map[string][]MarketSkip{}
	for _, g := range groups {
		if len(g.skipped) > 0 {
			out[g.Name] = g.skipped
		}
	}
	return out, nil
}

// PickerEntries is each fetched marketplace's entries, keyed by its name, with their installed
// state in sc: an entry is installed when sc's lock.json holds a module of its name, whichever
// marketplace it came from. It never fetches. A lock.json lola cannot read refuses the whole read
// (Ledger's coaching); warnings are MarketGroups'.
func (e *Engine) PickerEntries(sc Scope, display []string) (map[string][]PickerEntry, []string, error) {
	groups, warnings, err := e.MarketGroups(display)
	if err != nil {
		return nil, nil, err
	}
	installed, err := e.InstalledVersions(sc)
	if err != nil {
		return nil, nil, err
	}
	out := make(map[string][]PickerEntry, len(groups))
	for _, g := range groups {
		entries := make([]PickerEntry, len(g.Entries))
		for i, o := range g.Entries {
			at, ok := installed[o.Name]
			entries[i] = PickerEntry{MarketOffer: o, Installed: ok, At: at, WebURL: g.webURLs[i]}
		}
		out[g.Name] = entries
	}
	return out, warnings, nil
}
