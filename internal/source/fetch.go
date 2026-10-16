package source

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/trevor-vaughan/lola-go/internal/errs"
)

// Fetch validates spec, then stages its module content in a new directory under
// stagingParent (design §3).
func Fetch(ctx context.Context, spec Spec, stagingParent string) (Fetched, error) {
	return fetch(ctx, spec, stagingParent, limits{entries: maxEntries, bytes: maxBytes, ratio: maxRatio, body: maxBody})
}

func fetch(ctx context.Context, spec Spec, stagingParent string, lim limits) (Fetched, error) {
	if err := validate(spec); err != nil {
		return Fetched{}, err
	}
	if spec.Ref != "" && (spec.Kind == KindFolder || spec.Kind == KindArchive) {
		return Fetched{}, errs.WithHints(fmt.Errorf("--ref applies to git sources only, not to %s source %s", spec.Kind, printable(spec.Location, maxName)),
			"drop --ref: a "+string(spec.Kind)+" has no branches, tags or commits")
	}
	switch spec.Kind {
	case KindFolder:
		return fetchFolder(ctx, spec, stagingParent)
	case KindArchive:
		return inScratch(stagingParent, func(scratch string) (Fetched, error) {
			return fetchArchive(ctx, spec, scratch, stagingParent, lim)
		})
	case KindGit:
		return inScratch(stagingParent, func(scratch string) (Fetched, error) {
			return fetchGit(ctx, spec, scratch, stagingParent)
		})
	default:
		return Fetched{}, fmt.Errorf("source %s has unknown type %q", printable(spec.Location, maxName), printable(string(spec.Kind), maxName))
	}
}

// inScratch runs fetch with a new scratch directory under stagingParent for the remote
// party's tree, and removes that directory on success and on error.
func inScratch(stagingParent string, fetch func(scratch string) (Fetched, error)) (Fetched, error) {
	if err := os.MkdirAll(stagingParent, 0o700); err != nil {
		return Fetched{}, err
	}
	scratch, err := os.MkdirTemp(stagingParent, "scratch-*")
	if err != nil {
		return Fetched{}, err
	}
	f, err := fetch(scratch)
	if err := errors.Join(err, os.RemoveAll(scratch)); err != nil {
		if f.Dir != "" {
			err = errors.Join(err, os.RemoveAll(f.Dir))
		}
		return Fetched{}, err
	}
	return f, nil
}
