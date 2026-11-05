package native

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// GoBuild is the build information a Go toolchain embeds in its programs.
type GoBuild struct{ GoVersion, Module, ModuleVersion string }

// Detail is Binary plus what `lola info` shows about one file.
type Detail struct {
	Binary
	Libraries []string // always non-nil
	Linkage   string   // "static", "dynamic", "" when unknown
	Go        *GoBuild // nil when not a Go program
	SHA256    string   // hex digest of the whole file
}

// Describe inspects one file. It fails only when the file cannot be read or is not a native
// binary; a binary whose parser fails comes back Malformed, with its digest.
func Describe(root *os.Root, file string) (Detail, error) {
	f, b, ok, err := open(root, file)
	if f != nil {
		defer func() { _ = f.Close() }()
	}
	if err != nil {
		return Detail{}, err
	}
	if !ok {
		return Detail{}, fmt.Errorf("%s: not a native binary", untrusted.Text(file, untrusted.MaxName))
	}
	d := Detail{Binary: b, Libraries: []string{}}
	var imp imports
	parse(f, &d.Binary, &imp)
	if !d.Malformed && d.Size <= maxParse {
		d.Libraries = append(d.Libraries, imp.libraries...)
		d.Linkage = "static"
		if imp.dynamic {
			d.Linkage = "dynamic"
		}
		d.Go = goBuild(f)
	}
	h := sha256.New()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Detail{}, fmt.Errorf("%s: %w", untrusted.Text(file, untrusted.MaxName), untrusted.CleanErr(err))
	}
	if _, err := io.Copy(h, f); err != nil {
		return Detail{}, fmt.Errorf("%s: %w", untrusted.Text(file, untrusted.MaxName), untrusted.CleanErr(err))
	}
	d.SHA256 = hex.EncodeToString(h.Sum(nil))
	return d, nil
}

// goBuild returns the embedded Go build information, or nil when f is not a Go program or the
// reader fails (a panic included).
func goBuild(f *os.File) (g *GoBuild) {
	defer func() {
		if recover() != nil {
			g = nil
		}
	}()
	bi, err := buildinfo.Read(f)
	if err != nil {
		return nil
	}
	return &GoBuild{GoVersion: bi.GoVersion, Module: bi.Main.Path, ModuleVersion: bi.Main.Version}
}
