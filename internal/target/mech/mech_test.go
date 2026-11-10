package mech_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/target/mech"
	"github.com/trevor-vaughan/lola-go/internal/targetspec"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Resolve", func() {
	candidates := targetspec.FileRef{First: []string{"a.jsonc", "a.json"}}

	It("names a fixed path as it is", func() {
		root := testenv.Isolate()
		Expect(mech.Resolve("t", targetspec.FileRef{Path: "x.json"}, root)).To(Equal("x.json"))
	})

	It("falls back to the last candidate when none exists", func() {
		root := testenv.Isolate()
		Expect(mech.Resolve("t", candidates, root)).To(Equal("a.json"))
	})

	It("names the first candidate that exists", func() {
		root := testenv.Isolate()
		Expect(os.WriteFile(filepath.Join(root, "a.jsonc"), []byte("{}"), 0o644)).To(Succeed())
		Expect(mech.Resolve("t", candidates, root)).To(Equal("a.jsonc"))
	})

	It("fails, naming the candidate, when one cannot be checked", func() {
		if os.Geteuid() == 0 {
			Skip("root can read any directory")
		}
		root := testenv.Isolate()
		dir := filepath.Join(root, "locked")
		Expect(os.Mkdir(dir, 0o755)).To(Succeed())
		Expect(os.Chmod(dir, 0o000)).To(Succeed())
		DeferCleanup(os.Chmod, dir, os.FileMode(0o755))
		_, err := mech.Resolve("t", candidates, dir)
		Expect(err).To(MatchError(And(ContainSubstring("t: cannot read"), ContainSubstring(filepath.Join(dir, "a.jsonc")))))
	})
})
