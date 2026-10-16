package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// staleRun is the age at which a run directory is taken to be left by a process that was killed.
const staleRun = 24 * time.Hour

// runDir creates the private staging directory of one staging command (Plan 6a §6.3): its
// fetch scratch, fetched copies, version builds and snapshots all go inside, and the command
// removes it when it ends. Commands that report notices sweep first (sweepRuns).
func (e *Engine) runDir() (string, error) {
	if err := os.MkdirAll(e.Paths.Staging, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(e.Paths.Staging, "run-*")
}

// sweepRuns removes the run directories under Paths.Staging last modified more than staleRun
// ago, which killed processes left behind; a live run's directory is younger. It is best
// effort: what it cannot remove is a warning, never a refusal, and the next sweep tries again.
// lola cache add, which reports no notices, does not sweep.
func (e *Engine) sweepRuns() []Notice {
	es, err := os.ReadDir(e.Paths.Staging)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	for _, d := range es {
		if !d.IsDir() || !strings.HasPrefix(d.Name(), "run-") {
			continue
		}
		fi, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue // another lola removed it
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if e.Now().Sub(fi.ModTime()) > staleRun {
			errs = append(errs, os.RemoveAll(filepath.Join(e.Paths.Staging, d.Name())))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return []Notice{{Level: LevelWarn, Text: fmt.Sprintf("could not remove what a killed lola left in %s: %v; remove it by hand",
			e.Paths.Staging, untrusted.CleanErr(err))}}
	}
	return nil
}
