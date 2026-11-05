package module

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Templates (Plan 7 §3.1): a Markdown component whose file name ends in exactly .md.liquid is a
// Liquid template the engine renders per target; load reads its source, capped, and never
// parses it.
const (
	templateSuffix   = ".md.liquid"
	maxTemplates     = 256       // template files per module
	maxTemplateBytes = 256 << 10 // bytes of one template file
	skillTemplate    = "SKILL.md" + ".liquid"
)

// TemplateSource marks a Doc or Rule whose file is a template: File is its content-relative
// .md.liquid path.
type TemplateSource struct {
	File string
}

// IsTemplate reports whether name is a template's file name: it ends in exactly .md.liquid.
func IsTemplate(name string) bool { return strings.HasSuffix(name, templateSuffix) }

// ComponentName returns the name a component file gives its component: the base name without
// .md.liquid or .md.
func ComponentName(rel string) string {
	base := path.Base(rel)
	if n, ok := strings.CutSuffix(base, templateSuffix); ok {
		return n
	}
	return strings.TrimSuffix(base, ".md")
}

// suffixVariant reports whether name ends in .md.liquid only under case folding, which a lola
// template never does.
func suffixVariant(name string) bool {
	return !IsTemplate(name) && strings.HasSuffix(strings.ToLower(name), templateSuffix)
}

// suffixWarning is the warning for a file whose suffix is a case variant of .md.liquid.
func suffixWarning(rel string) string {
	return safeName(rel) + " is not a template: the suffix is " + templateSuffix
}

// readTemplate reads the template at the content-relative rel for m as readCapped does, and m
// may hold at most maxTemplates. A literal file of the same name beside it, under case folding,
// refuses the module.
func readTemplate(dir, rel string, m *Module) (string, error) {
	if m.templates++; m.templates > maxTemplates {
		return "", fmt.Errorf("%d template files; a module has at most %d", m.templates, maxTemplates)
	}
	if err := sideBySide(dir, rel); err != nil {
		return "", err
	}
	return readCapped(dir, rel, "template")
}

// readCapped reads the Liquid source at the content-relative rel, a template or a partial
// (kind), as readRegular does, capped at maxTemplateBytes, and it must be UTF-8.
func readCapped(dir, rel, kind string) (string, error) {
	b, err := readRegular(dir, rel, maxTemplateBytes, irregularFile(rel, kind), func(size string) error { return tooLarge(rel, kind, size) })
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("%s:%d: is not UTF-8; a %s file must be UTF-8 text", safeName(rel), invalidLine(b), kind)
	}
	return string(b), nil
}

// readRegular reads the content-relative rel, at most limit bytes, only from a regular file: a
// file that is not one refuses as irregular, and its size is checked from Lstat and again
// through a limited read, refusing as tooBig does with the size, before anything parses it.
func readRegular(dir, rel string, limit int64, irregular error, tooBig func(size string) error) ([]byte, error) {
	full := filepath.Join(dir, filepath.FromSlash(rel))
	fi, err := os.Lstat(full)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", safeName(rel), untrusted.CleanErr(err))
	}
	if !fi.Mode().IsRegular() { // a symbolic link, a FIFO, a device: never opened
		return nil, irregular
	}
	if fi.Size() > limit {
		return nil, tooBig(fmt.Sprintf("%d bytes", fi.Size()))
	}
	// non-blocking, so a FIFO swapped in after Lstat opens and is refused instead of waiting
	f, err := os.OpenFile(full, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", safeName(rel), untrusted.CleanErr(err))
	}
	opened, err := f.Stat()
	var b []byte
	switch {
	case err != nil:
	case !opened.Mode().IsRegular(): // checked on the file opened, which no rename can swap
		err = irregular
	case !os.SameFile(fi, opened): // swapped, for a link or anything else, after Lstat
		err = errors.New("changed while lola was reading it")
	default:
		b, err = io.ReadAll(io.LimitReader(f, limit+1))
	}
	err = errors.Join(err, f.Close())
	switch {
	case errors.Is(err, irregular):
		return nil, irregular
	case err != nil:
		return nil, fmt.Errorf("%s: %w", safeName(rel), untrusted.CleanErr(err))
	case int64(len(b)) > limit:
		return nil, tooBig(fmt.Sprintf("over %d KiB", limit>>10)) // the file grew after Stat; the read stopped past the cap
	}
	return b, nil
}

// tooLarge refuses the template or partial (kind) at the content-relative rel, whose size is
// over maxTemplateBytes.
func tooLarge(rel, kind, size string) error {
	return fmt.Errorf("%s: is %s; a %s file is at most %d KiB", safeName(rel), size, kind, maxTemplateBytes>>10)
}

// irregularFile refuses the template or partial (kind) at the content-relative rel, which is a
// directory, a symbolic link, a FIFO or a device: lola reads Liquid only from a regular file.
func irregularFile(rel, kind string) error {
	return fmt.Errorf("%s: a %s must be a regular file", safeName(rel), kind)
}

// invalidLine is the 1-based line of b's first byte that is not UTF-8.
func invalidLine(b []byte) int {
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			return strings.Count(string(b[:i]), "\n") + 1
		}
		i += size
	}
	return 0
}

// sideBySide refuses a template whose directory also holds the literal file it would render,
// comparing names with case folded, as a case-insensitive file system would.
func sideBySide(dir, rel string) error {
	parent, name := path.Split(rel)
	literal := strings.TrimSuffix(name, ".liquid")
	es, err := os.ReadDir(filepath.Join(dir, filepath.FromSlash(parent)))
	if err != nil {
		return fmt.Errorf("%s: %w", safeName(rel), untrusted.CleanErr(err))
	}
	for _, e := range es {
		if !strings.EqualFold(e.Name(), literal) {
			continue
		}
		label := path.Join(parent, ComponentName(name))
		if name == skillTemplate {
			label = path.Clean(parent)
		}
		return fmt.Errorf("%s: both %s and %s exist; keep one", safeName(label), safeName(e.Name()), safeName(name))
	}
	return nil
}

// readComponent reads a component file: a template (IsTemplate) unparsed, else as readDoc does.
func readComponent(dir, rel string, m *Module) (Doc, error) {
	if !IsTemplate(rel) {
		return readDoc(dir, rel)
	}
	body, err := readTemplate(dir, rel, m)
	if err != nil {
		return Doc{}, err
	}
	return Doc{Body: body, Template: &TemplateSource{File: rel}}, nil
}

// skillFiles lists the entries of the content-relative directory rel named exactly SKILL.md or
// SKILL.md.liquid, of any type, sorted, and the names of those that are only case variants of
// either, which lola never reads (variantWarning). It decides from the names the directory
// lists, never from Lstat, which a case-insensitive file system answers for skill.md too.
func skillFiles(dir, rel string) (files []fs.DirEntry, variants []string, err error) {
	es, err := os.ReadDir(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, nil, ioFail(rel, err)
	}
	for _, e := range es {
		switch n := e.Name(); {
		case n == "SKILL.md" || n == skillTemplate:
			files = append(files, e)
		case strings.EqualFold(n, "SKILL.md") || strings.EqualFold(n, skillTemplate):
			variants = append(variants, n)
		}
	}
	return files, variants, nil
}

// variantWarning is the warning for name, a case variant of SKILL.md or SKILL.md.liquid in the
// content-relative directory rel.
func variantWarning(rel, name string) string {
	want := "SKILL.md"
	if strings.EqualFold(name, skillTemplate) {
		want = skillTemplate
	}
	return safeName(path.Join(rel, name)) + " is not read; name it " + want
}

// skillDoc reads the skill in the content-relative directory rel: its SKILL.md or
// SKILL.md.liquid, the case variants of those names it holds, which are excluded from its copy
// with a warning, and its templated support files. ok is false when it has neither as a
// regular file.
func skillDoc(dir, rel string, m *Module, s *Skill) (ok bool, err error) {
	files, variants, err := skillFiles(dir, rel)
	if err != nil {
		return false, err
	}
	for _, n := range variants {
		s.Exclude = append(s.Exclude, n)
		m.Warnings = append(m.Warnings, variantWarning(rel, n))
	}
	file := ""
	for _, e := range files {
		if e.Type().IsRegular() || len(files) == 2 {
			file = e.Name() // with both, SKILL.md.liquid, which sorts last: readTemplate refuses the pair
		}
	}
	if file == "" {
		return false, nil
	}
	if file == skillTemplate && !slices.Contains(s.Exclude, skillTemplate) {
		s.Exclude = append(s.Exclude, skillTemplate) // its target writes SKILL.md
	}
	if s.Doc, err = readComponent(dir, path.Join(rel, file), m); err != nil {
		return false, err
	}
	s.Files, err = supportFiles(dir, rel, s.Exclude, m)
	return true, err
}

// supportFiles reads the templated support files of the skill in the content-relative
// directory rel: every *.md.liquid file below it but SKILL.md.liquid and the excluded
// Dir-relative paths, which its copy skips too. A file whose suffix is a case variant warns.
func supportFiles(dir, rel string, exclude []string, m *Module) ([]SkillFile, error) {
	var out []SkillFile
	root := filepath.Join(dir, filepath.FromSlash(rel))
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		r, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		r = filepath.ToSlash(r)
		switch {
		case walkErr != nil:
			return fmt.Errorf("%s: %w", safeName(path.Join(rel, r)), untrusted.CleanErr(walkErr))
		case p == root:
			return nil
		case slices.Contains(exclude, r):
			if d.IsDir() {
				return fs.SkipDir
			}
		case r == skillTemplate:
		case IsTemplate(d.Name()) && !d.Type().IsRegular():
			_, err := readTemplate(dir, path.Join(rel, r), m) // refuses it: a template must be a regular file
			return err
		case d.IsDir(), !d.Type().IsRegular():
		case suffixVariant(d.Name()):
			m.Warnings = append(m.Warnings, suffixWarning(path.Join(rel, r)))
		case IsTemplate(d.Name()):
			body, err := readTemplate(dir, path.Join(rel, r), m)
			if err != nil {
				return err
			}
			out = append(out, SkillFile{Rel: strings.TrimSuffix(r, ".liquid"), Source: r, Body: body})
		}
		return nil
	})
	return out, err
}

// configTemplates warns about module-root files named as configuration lola reads, or as the
// Markdown files a root skill never holds (rootDocs), plus .liquid: only Markdown components are
// templates.
func configTemplates(dir string, m *Module) error {
	for _, n := range slices.Concat(ManifestNames(), []string{"mcps.json", ".mcp.json", "mcp.json", "plugin.json"}, rootDocs()) {
		_, err := os.Lstat(filepath.Join(dir, n+".liquid"))
		switch {
		case err == nil:
			m.Warnings = append(m.Warnings, n+".liquid is not read: only Markdown components are templates")
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("%s: %w", safeName(n+".liquid"), untrusted.CleanErr(err))
		}
	}
	return nil
}
