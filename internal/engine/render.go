package engine

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trust"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// The codes of the findings render leaves a module out with (Plan 6a §10.3).
const (
	CodeNotInManifest  = "not-in-manifest" // project.yml no longer lists the module
	CodeSourceDiffers  = "source-differs"  // the manifest, or the cache for a folder, names another source
	CodeNotTrusted     = "not-trusted"     // the trust record does not cover the entry
	CodeContentChanged = "content-changed" // a folder module's content is not the version installed
)

// leftOutCodes are the codes above, which --strict coaches with each module's own remedies.
var leftOutCodes = []string{CodeNotInManifest, CodeSourceDiffers, CodeNotTrusted, CodeContentChanged}

// leftOut is a module render leaves as installed: the warn finding that says why, and its
// remedies, which the finding's hint joins and --strict lists one by one.
type leftOut struct {
	finding target.Finding
	hints   []string
}

func newLeftOut(name, code, text string, hints ...string) *leftOut {
	return &leftOut{finding: target.Finding{Severity: target.SeverityWarn, Module: untrusted.Text(name, untrusted.MaxName), Kind: "module",
		Code: code, Text: text, Hint: strings.Join(hints, "; ")}, hints: hints}
}

// renderCheck decides, locally and before anything is fetched, whether render may act on the
// ledger module name (Plan 6a §10.3): the first check that applies leaves it out as installed.
// At project scope: a module the manifest no longer lists; an entry that asks for another
// source, ref or path than lock.json records, or a folder whose cache records another source
// (at user scope too); an entry rec does not cover, or that installs to top-level targets rec
// does not cover; a folder whose content does not load or is not the version installed, which
// it reads into run and returns staged, uncached, when render may install it.
func (e *Engine) renderCheck(ctx context.Context, sc Scope, man *manifest.Manifest, rec *trust.Record, name string, entry *ledger.Entry,
	run string) (*leftOut, *store.Staged, error) {
	base := filepath.Base(e.files(sc).manifest)
	ment := man.Find(name)
	review := "review and apply it: " + e.SyncCommand(sc, name)
	if sc.Kind == target.Project && ment == nil {
		return newLeftOut(name, CodeNotInManifest, base+" no longer lists it", "review: "+e.SyncCommand(sc, name)), nil, nil
	}
	if ment != nil && (ment.Source != entry.Source || ment.Ref != entry.Ref || ment.Path != entry.Path) {
		return newLeftOut(name, CodeSourceDiffers, base+" asks for another source than is installed", review), nil, nil
	}
	if moved, err := e.folderMoved(sc, name, entry); err != nil || moved != "" {
		if err != nil {
			return nil, nil, err
		}
		return newLeftOut(name, CodeSourceDiffers, "your cache now has it from "+untrusted.Text(moved, untrusted.MaxName)+
			", not the folder it was installed from", review), nil, nil
	}
	if sc.Kind != target.Project {
		return nil, nil, nil
	}
	if !rec.View.Covers(*ment) || len(ment.Targets) == 0 && !slices.Equal(rec.View.Targets, man.Targets) {
		return newLeftOut(name, CodeNotTrusted, base+" "+untrustedWhy(rec, name), review), nil, nil
	}
	spec, ok, err := source.Parse(ment.Source, sourceRoot(sc))
	if err != nil || !ok || spec.Kind != source.KindFolder {
		return nil, nil, nil // not a folder, or one that is gone: rendered at the version installed
	}
	spec.Path = ment.Path
	st, err := e.prepareFetch(ctx, name, spec, run)
	if err != nil { // a pulled edit that no longer loads: the sync it hints shows the error, or refuses it
		why := strings.TrimPrefix(untrusted.CleanErr(err).Error(), untrusted.Text(name, untrusted.MaxName)+": ")
		return newLeftOut(name, CodeContentChanged, "its content no longer loads: "+shown(why),
			append([]string{"review it: " + e.SyncCommand(sc, name), "or remove it: " + e.Command(sc, "uninstall", name)}, errs.Hints(err)...)...), nil, nil
	}
	if st.Version.Meta.Digest != entry.Digest {
		return newLeftOut(name, CodeContentChanged, "its content changed since lola installed it",
			"review it: "+e.SyncCommand(sc, name), "edited it yourself? "+e.installFolder(sc, spec.Location)), nil, nil
	}
	return nil, &st, nil
}

// folderMoved is the source the cache records for a folder module when it is not the folder
// lock.json says the module was installed from, else "" (Plan 6a §10.3 check 2): a folder has
// no pin, so a cached source re-pointed to another project's folder (§6.5) would render that
// folder here.
func (e *Engine) folderMoved(sc Scope, name string, entry *ledger.Entry) (string, error) {
	o, found, err := e.Store.Origin(name)
	if err != nil || !found || o.Type != string(source.KindFolder) {
		return "", err
	}
	recorded := entry.Source
	if !filepath.IsAbs(recorded) {
		recorded = filepath.Join(sourceRoot(sc), recorded)
	}
	if source.SameLocation(source.KindFolder, recorded, o.Source) && o.Path == entry.Path {
		return "", nil
	}
	return describeSource(o.Source, o.Path), nil
}

// renderStaged is the folder version renderCheck read for render: committed to the cache, as
// every render of a folder caches it, and rendered from the staged tree that was hashed, with
// the warnings a cache hit reports.
func (e *Engine) renderStaged(st *store.Staged, entry *ledger.Entry) (store.Version, error) {
	if _, err := e.Store.Commit(*st, false); err != nil {
		return store.Version{}, err
	}
	v := st.Version
	v.Module.Warnings = st.LoadWarnings
	v.Commit = entry.Commit
	return v, nil
}

// renderPin is the version render installs for a module whose manifest entry ment pins one
// (Plan 6a §10.3): the pin, else lock.json's version. repin is the sync that re-pins the entry,
// which coaches a pinned fetch that fails; "" when render installs lock.json's version.
func (e *Engine) renderPin(sc Scope, name string, entry *ledger.Entry, ment *manifest.Entry) (digest, commit, repin string) {
	if ment == nil || ment.Digest == "" {
		return entry.Digest, entry.Commit, ""
	}
	return ment.Digest, ment.Commit, e.SyncCommand(sc, "--update", name)
}

// renderError coaches a failed fetch of the pin render installs: a digest mismatch or a commit
// not on the ref is re-pinned by sync, never installed from the source unseen (Plan 6a §11.3).
func (e *Engine) renderError(sc Scope, name string, ment *manifest.Entry, repin string, err error) error {
	if repin == "" {
		return err
	}
	return syncPinError(name, filepath.Base(e.files(sc).manifest), *ment, err, repin)
}
