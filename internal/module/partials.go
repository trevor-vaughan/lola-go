package module

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/templating"
)

// Partials (Plan 7 §3.3): the Liquid files a module's templates include, read from _partials/ at
// the module root, flat, as _partials/<name>.liquid or _partials/<name>.<target>.liquid.
const (
	partialsDir      = templating.PartialsDir
	maxPartials      = 256     // partial files per module
	maxPartialsBytes = 4 << 20 // bytes of a module's partials together
)

// loadPartials reads m's partials into m.Partials, keyed by content-relative path. Another entry
// of _partials/ warns and is ignored; so does a case variant of _partials at the module root,
// which lola never reads. known lists the target ids a partial's name may end in.
func loadPartials(dir string, m *Module, known []string) error {
	variants, err := partialsVariants(dir)
	if err != nil {
		return err
	}
	for _, n := range variants {
		m.Warnings = append(m.Warnings, safeName(n)+" is not read; name it "+partialsDir)
	}
	fi, err := exactEntry(dir, partialsDir)
	switch {
	case err != nil:
		return err
	case fi == nil:
		return nil
	case !fi.IsDir(): // a file, or a symbolic link lola never follows
		return errs.WithHints(errors.New(partialsDir+" is not a directory; partials are "+partialsDir+"/<name>.liquid files"),
			"rename the "+partialsDir+" file, or move it into a "+partialsDir+"/ directory")
	}
	es, err := os.ReadDir(filepath.Join(dir, partialsDir))
	if err != nil {
		return ioFail(partialsDir, err)
	}
	var files []fs.DirEntry
	size := int64(0)
	for _, e := range es {
		if e.IsDir() || !partialFile(e.Name(), known) {
			m.Warnings = append(m.Warnings, safeName(partialsDir+"/"+e.Name())+" is not a partial ("+partialsDir+"/<name>.liquid or <name>.<target>.liquid); ignored")
			continue
		}
		files = append(files, e)
		if fi, err := e.Info(); err == nil { // else readCapped reports why it cannot be read
			if fi.Mode().IsRegular() && fi.Size() > maxTemplateBytes { // named before the total it also passes
				return tooLarge(partialsDir+"/"+e.Name(), "partial", fmt.Sprintf("%d bytes", fi.Size()))
			}
			size += fi.Size()
		}
	}
	overTotal := func(total int64) error {
		return errs.WithHints(fmt.Errorf("%s: the partials hold %d bytes; a module's partials are at most %d MiB together", partialsDir, total, maxPartialsBytes>>20),
			"move or remove files in "+partialsDir+"/")
	}
	switch {
	case len(files) > maxPartials:
		return errs.WithHints(fmt.Errorf("%d partial files; a module has at most %d", len(files), maxPartials), "move or remove files in "+partialsDir+"/")
	case size > maxPartialsBytes:
		return overTotal(size)
	}
	total := int64(0)
	for _, e := range files {
		rel := partialsDir + "/" + e.Name()
		body, err := readCapped(dir, rel, "partial")
		if err != nil {
			return err
		}
		total += int64(len(body))
		if m.Partials == nil {
			m.Partials = map[string]string{}
		}
		m.Partials[rel] = body
	}
	if total > maxPartialsBytes { // a file grew after it was listed
		return overTotal(total)
	}
	return nil
}

// exactEntry describes the entry of the directory dir named exactly name, as Lstat would, or
// nil when it has none: on a file system that folds case, Lstat(name) would find a variant.
func exactEntry(dir, name string) (fs.FileInfo, error) {
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil, ioFail("the module root", err)
	}
	for _, e := range es {
		if e.Name() == name {
			fi, err := e.Info()
			if err != nil {
				return nil, ioFail(name, err)
			}
			return fi, nil
		}
	}
	return nil, nil
}

// partialsVariants lists the entries of the module root at dir whose names are case variants of
// _partials, as spelled on disk: lola never reads them, and a root skill never copies them.
func partialsVariants(dir string) ([]string, error) {
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil, ioFail("the module root", err)
	}
	var out []string
	for _, e := range es {
		if e.Name() != partialsDir && strings.EqualFold(e.Name(), partialsDir) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// rootExclude is what the skill at the module root at dir never copies: the module-level entries
// (rootSkillExclude) and the case variants of _partials there.
func rootExclude(dir string) ([]string, error) {
	variants, err := partialsVariants(dir)
	return append(rootSkillExclude(), variants...), err
}

// partialFile reports whether file is a partial's file name: <name>.liquid or
// <name>.<target>.liquid, the target one of known.
func partialFile(file string, known []string) bool {
	base, ok := strings.CutSuffix(file, ".liquid")
	if !ok {
		return false
	}
	name, target, targeted := strings.Cut(base, ".")
	return templating.PartialName(name) && (!targeted || slices.Contains(known, target))
}
