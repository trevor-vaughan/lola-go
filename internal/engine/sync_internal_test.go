package engine

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("syncPinError", func() {
	ent := manifest.Entry{Name: "example", Source: "https://example.com/x.git", Ref: "main", Commit: strings.Repeat("a", 40)}

	It("re-pins after a failed fetch of the pinned commit only if the server refuses fetching by commit", func() {
		testenv.Isolate()
		err := syncPinError("example", "project.yml", ent, &commitFetchError{name: "example", err: errors.New("fetch failed")}, "lola sync --update example")
		Expect(err).To(MatchError("example: fetch failed"), "the module is named once")
		Expect(errs.Hints(err)).To(Equal([]string{"if the server refuses fetching by commit, re-pin to what main serves now: lola sync --update example"}))
	})

	It("never coaches a sync to install from the source when the source serves another commit than the pin", func() {
		testenv.Isolate()
		served := errs.WithHints(&servedCommitError{name: "example", location: ent.Source, got: strings.Repeat("b", 40), want: ent.Commit},
			"install what its source serves now: lola install "+ent.Source)
		err := syncPinError("example", "project.yml", ent, served, "lola sync --update example")
		Expect(err).To(MatchError("example: " + ent.Source + " served commit " + strings.Repeat("b", 40) + " for " + ent.Commit))
		Expect(errs.Hints(err)).To(Equal([]string{"check the source before you re-pin it: lola sync --update example"}))
	})
})

var _ = Describe("the trust diff's parts", func() {
	It("quotes an MCP server's cwd as it quotes each argument, so where it ends stays visible", func() {
		testenv.Isolate()
		srv := contentMCPServer(module.MCPServer{Name: "x", Transport: module.TransportStdio, Command: "x", Cwd: "my tools"})
		Expect(srv.Cwd).To(Equal(`"my tools"`))
		Expect(contentMCPServer(module.MCPServer{Name: "x", Transport: module.TransportStdio, Command: "x", Cwd: "tools"}).Cwd).To(Equal("tools"))
	})

	It("reads a Windows file:///C:/ URL's path without its leading slash", func() {
		testenv.Isolate()
		if runtime.GOOS != "windows" {
			Skip("a drive letter starts a path only on Windows")
		}
		sc := Scope{Kind: target.Project, ProjectDir: `C:\proj`}
		Expect(lexicallyInside(sc, "file:///C:/proj/mods/x")).To(BeTrue())
		Expect(lexicallyInside(sc, "file:///C:/elsewhere/x")).To(BeFalse())
		Expect(filepath.IsAbs(`C:\proj`)).To(BeTrue())
	})

	It("joins the scopes an install did not reach as prose", func() {
		testenv.Isolate()
		Expect(andJoin([]string{"the project at /a"})).To(Equal("the project at /a"))
		Expect(andJoin([]string{"the project at /a", "the user scope"})).To(Equal("the project at /a and the user scope"))
		Expect(andJoin([]string{"a", "b", "c"})).To(Equal("a, b and c"))
	})
})
