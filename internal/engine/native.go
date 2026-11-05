package engine

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/native"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// nativeShown is how many executables the finding names before it counts the rest.
const nativeShown = 3

// nativeFindings is the one module-level finding that says the module installs native
// executables, from a scan of exactly the files its targets copy (installedFiles). Its hint
// names the lola info command for infoArg, the argument that reaches this module, with display.
func nativeFindings(contentDir string, m *module.Module, infoArg string, display []string) ([]target.Finding, error) {
	files, err := installedFiles(contentDir, m)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(contentDir)
	if err != nil {
		return nil, untrusted.CleanErr(err)
	}
	defer func() { _ = root.Close() }()
	bins, err := native.Find(root, files)
	if err != nil || len(bins) == 0 {
		return nil, err
	}
	parts := make([]string, 0, nativeShown)
	for _, b := range bins[:min(nativeShown, len(bins))] {
		parts = append(parts, untrusted.Text(b.Path, untrusted.MaxName)+" ("+describeBinary(b)+")")
	}
	noun := "executable"
	if len(bins) != 1 {
		noun = "executables"
	}
	text := fmt.Sprintf("ships %d native %s: %s", len(bins), noun, strings.Join(parts, ", "))
	if len(bins) > nativeShown {
		text += fmt.Sprintf(" and %d more", len(bins)-nativeShown)
	}
	return []target.Finding{{Severity: target.SeverityWarn, Module: m.Name, Kind: "module", Code: CodeNativeExecutable, Text: text,
		Hint: errs.ShellJoin(append([]string{"lola", "info", infoArg}, display...)) + " lists each with the libraries it loads and its sha256"}}, nil
}

// describeBinary is what a finding says a binary is: its format, kind, architectures and OS.
func describeBinary(b native.Binary) string {
	if b.Malformed {
		return "looks like " + b.Format.WithArticle() + " file but is malformed"
	}
	s := string(b.Format) + " " + b.Kind
	if len(b.Arch) > 0 {
		s += ", " + strings.Join(b.Arch, "/")
	}
	if b.OS != "" {
		s += " " + b.OS
	}
	return s
}

// installedFiles lists the regular files, as sorted slash paths relative to contentDir, that
// the targets copy from the module's skills and command sidecar directories. It mirrors
// target.SkillTree and target.SidecarTree and the exclusion rule of plan's copy: an excluded
// path is an exact tree-relative match, and an excluded directory takes its subtree with it.
// Anything that is not a regular file (a link, a FIFO) is left out: native.Find opens what it is given.
func installedFiles(contentDir string, m *module.Module) ([]string, error) {
	root, err := os.OpenRoot(contentDir)
	if err != nil {
		return nil, untrusted.CleanErr(err)
	}
	defer func() { _ = root.Close() }()
	var out []string
	walk := func(dir string, exclude []string) error {
		return fs.WalkDir(root.FS(), dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || p == dir {
				return err
			}
			rel := strings.TrimPrefix(p, dir+"/")
			if dir == "." {
				rel = p
			}
			if slices.Contains(exclude, rel) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.Type().IsRegular() {
				out = append(out, p)
			}
			return nil
		})
	}
	for _, s := range m.Skills {
		exclude := slices.Clone(s.Exclude)
		exclude = append(exclude, "SKILL.md")
		for _, f := range s.Files {
			exclude = append(exclude, f.Source)
		}
		if err := walk(path.Clean(s.Dir), exclude); err != nil {
			return nil, untrusted.CleanErr(err)
		}
	}
	for _, c := range m.Commands {
		if c.SidecarDir == "" {
			continue
		}
		var exclude []string
		for _, sc := range c.Sidecars {
			if sc.Source != "" {
				exclude = append(exclude, strings.TrimPrefix(sc.Source, c.SidecarDir+"/"))
			}
		}
		if err := walk(path.Clean(c.SidecarDir), exclude); err != nil {
			return nil, untrusted.CleanErr(err)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// InfoNative is one native binary a module installs, as lola info shows it. Go is nil when the
// binary is not a Go program; Linkage is "" and Libraries empty when it could not be read further.
type InfoNative struct {
	Path      string   `json:"path"`
	Format    string   `json:"format"`
	Kind      string   `json:"kind"`
	Arch      []string `json:"arch"`
	OS        string   `json:"os"`
	Malformed bool     `json:"malformed"`
	Size      int64    `json:"size"`
	Libraries []string `json:"libraries"`
	Linkage   string   `json:"linkage"`
	Go        *InfoGo  `json:"go"`
	SHA256    string   `json:"sha256"`
}

// InfoGo is the build information a Go program carries.
type InfoGo struct {
	GoVersion     string `json:"go_version"`
	Module        string `json:"module"`
	ModuleVersion string `json:"module_version"`
}

// infoNative lists the native binaries among the files the module's targets copy, each described.
// Every text field the module supplies passes through untrusted.Text.
func infoNative(contentDir string, m *module.Module) ([]InfoNative, error) {
	out := []InfoNative{}
	files, err := installedFiles(contentDir, m)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(contentDir)
	if err != nil {
		return nil, untrusted.CleanErr(err)
	}
	defer func() { _ = root.Close() }()
	bins, err := native.Find(root, files)
	if err != nil {
		return nil, err
	}
	for _, b := range bins {
		// Describe fails for a file Find matched only on a read error in between; the Find data stands.
		d, derr := native.Describe(root, b.Path)
		if derr != nil {
			d = native.Detail{Binary: b, Libraries: []string{}}
		}
		n := InfoNative{Path: untrusted.Text(d.Path, untrusted.MaxName), Format: string(d.Format), Kind: d.Kind, Arch: []string{},
			OS: d.OS, Malformed: d.Malformed, Size: d.Size, Libraries: []string{}, Linkage: d.Linkage, SHA256: d.SHA256}
		n.Arch = append(n.Arch, d.Arch...)
		for _, l := range d.Libraries {
			n.Libraries = append(n.Libraries, untrusted.Text(l, untrusted.MaxName))
		}
		if d.Go != nil {
			n.Go = &InfoGo{GoVersion: untrusted.Text(d.Go.GoVersion, untrusted.MaxName), Module: untrusted.Text(d.Go.Module, untrusted.MaxName),
				ModuleVersion: untrusted.Text(d.Go.ModuleVersion, untrusted.MaxName)}
		}
		out = append(out, n)
	}
	return out, nil
}
