package migrate_test

import (
	"archive/zip"
	"io/fs"
	"os"
	"path/filepath"

	. "github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
)

// rec is one installed.yml record as the Python lola writes it.
type rec map[string]any

// pyHome is a Python lola home under a spec's temp dir.
type pyHome struct{ dir string }

// install writes installed.yml with recs, the way InstallationRegistry._save does.
func (h pyHome) install(recs ...rec) {
	b, err := yaml.Marshal(map[string]any{"version": "1.0", "installations": recs})
	Expect(err).NotTo(HaveOccurred())
	writeFile(filepath.Join(h.dir, "installed.yml"), string(b))
}

// source writes modules/<module>/.lola/source.yml.
func (h pyHome) source(module string, fields map[string]string) {
	b, err := yaml.Marshal(fields)
	Expect(err).NotTo(HaveOccurred())
	writeFile(filepath.Join(h.dir, "modules", module, ".lola", "source.yml"), string(b))
}

// writeFile creates path with content, making its parent directories.
func writeFile(path, content string) {
	Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
	Expect(os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
}

// writeTree creates files under root.
func writeTree(root string, files map[string]string) {
	for p, c := range files {
		writeFile(filepath.Join(root, filepath.FromSlash(p)), c)
	}
}

// zipTree writes a zip archive at file holding the files below root.
func zipTree(file, root string) {
	f, err := os.Create(file)
	Expect(err).NotTo(HaveOccurred())
	w := zip.NewWriter(f)
	r, err := os.OpenRoot(root)
	Expect(err).NotTo(HaveOccurred())
	Expect(fs.WalkDir(r.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		out, err := w.Create(p)
		Expect(err).NotTo(HaveOccurred())
		b, err := r.ReadFile(p)
		Expect(err).NotTo(HaveOccurred())
		_, err = out.Write(b)
		return err
	})).To(Succeed())
	Expect(r.Close()).To(Succeed())
	Expect(w.Close()).To(Succeed())
	Expect(f.Close()).To(Succeed())
}

// installAssistants puts claude, gemini and opencode on the spec's PATH (testenv.Isolate's bin/
// under root), so lola migrate finds every assistant it has a target for installed (Plan 10 §3.2).
func installAssistants(root string) {
	for _, name := range []string{"claude", "gemini", "opencode"} {
		writeFile(filepath.Join(root, "bin", name), "#!/bin/sh\n")
		Expect(os.Chmod(filepath.Join(root, "bin", name), 0o755)).To(Succeed())
	}
}

// newEngine is an engine over the spec's isolated environment.
func newEngine() *engine.Engine {
	p, err := paths.Resolve(os.Getenv)
	Expect(err).NotTo(HaveOccurred())
	return engine.New(p)
}

// reviewModule is a lola-go module with every component kind the Python lola records.
var reviewModule = map[string]string{
	"skills/review/SKILL.md":      "---\ndescription: Review code\n---\nReview it.\n",
	"skills/review/ref/checks.md": "checks\n",
	"commands/ship.md":            "---\ndescription: Ship it\n---\nShip $ARGUMENTS\n",
	"commands/ship/notes.txt":     "sidecar\n",
	"agents/critic.md":            "---\ndescription: critic\n---\nCriticize.\n",
	"mcps.json":                   `{"mcpServers":{"lola-gh":{"command":"gh-mcp","args":["--stdio"]}}}`,
}
