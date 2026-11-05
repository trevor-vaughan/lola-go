package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("fetchEntry", func() {
	It("refuses an archive whose bytes are not the sha256 its catalog lists, coaching the marketplace's --update", func() {
		tmp := testenv.Isolate()
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, err := zw.Create("bundle/commands/b.md")
		Expect(err).NotTo(HaveOccurred())
		_, err = w.Write([]byte("B\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(zw.Close()).To(Succeed())
		// A catalog's archive is at a host by name; its download, here on loopback, is what the
		// sha256 is checked against.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(buf.Bytes()) }))
		DeferCleanup(srv.Close)
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e := New(p)
		listed := strings.Repeat("AB", 32)
		pick := &marketPick{reg: market.Registration{Name: "acme", Source: "https://example.com/plugins.git"}, display: []string{"--json"},
			entry: market.Entry{Name: "bundle", Kind: "archive", Source: manifest.Entry{Name: "bundle", Source: srv.URL + "/bundle.zip"},
				ArchiveSHA256: strings.ToLower(listed)}}
		spec, commit := pick.spec("")
		run := filepath.Join(tmp, "run")
		_, err = e.fetchEntry(context.Background(), pick, spec, commit, run)
		Expect(err).To(MatchError(fmt.Sprintf("marketplace acme lists bundle with sha256 %s; the download is %x", strings.ToLower(listed),
			sha256.Sum256(buf.Bytes()))))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --update --json"}))
		left, err := os.ReadDir(run)
		Expect(err).NotTo(HaveOccurred())
		Expect(left).To(BeEmpty(), "nothing was extracted")

		// sync --update following the catalog fetches through the same checks (Plan 6c §6.6).
		se := &syncEntry{ent: manifest.Entry{Name: "bundle", Source: spec.Location}, spec: spec, update: true, follow: pick}
		err = e.resolveSync(context.Background(), Scope{Kind: target.User}, se, run)
		Expect(err).To(MatchError(ContainSubstring("marketplace acme lists bundle with sha256 " + strings.ToLower(listed) + "; the download is ")))
		Expect(errs.Hints(err)).To(Equal([]string{"lola market acme --update --json"}))

		pick.entry.ArchiveSHA256 = fmt.Sprintf("%x", sha256.Sum256(buf.Bytes()))
		spec, commit = pick.spec("")
		f, err := e.fetchEntry(context.Background(), pick, spec, commit, run)
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(f.Dir, "commands", "b.md")).To(BeARegularFile())
	})
})

var _ = Describe("pythonCatalog", func() {
	It("names a lone root YAML file that is a symbolic link as one, never as an irregular file", func() {
		tree := testenv.Isolate()
		Expect(os.WriteFile(filepath.Join(tree, "real.txt"), []byte("name: m\n"), 0o644)).To(Succeed())
		Expect(os.Symlink("real.txt", filepath.Join(tree, "catalog.yml"))).To(Succeed())
		_, err := pythonCatalog(tree, "m")
		Expect(err).To(MatchError("catalog.yml is a symbolic link; lola never follows links in a marketplace"))
	})
})
