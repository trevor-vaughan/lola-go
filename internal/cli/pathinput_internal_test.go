package cli

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("path input", func() {
	var home, cwd string

	BeforeEach(func() {
		root := testenv.Isolate()
		home, cwd = filepath.Join(root, "home"), filepath.Join(root, "work")
		for _, d := range []string{"home/projects", "home/photos", "home/.config", "work/app", "work/apple"} {
			Expect(os.MkdirAll(filepath.Join(root, d), 0o750)).To(Succeed())
		}
		Expect(os.WriteFile(filepath.Join(root, "home", "pnotes.txt"), nil, 0o600)).To(Succeed())
	})

	It("expands a leading ~ and ~/ to home and leaves ~name and inner ~ alone", func() {
		Expect(expandHome("~", home)).To(Equal(home))
		Expect(expandHome("~/a/b", home)).To(Equal(filepath.Join(home, "a", "b")))
		Expect(expandHome("~other/a", home)).To(Equal("~other/a"))
		Expect(expandHome("a/~/b", home)).To(Equal("a/~/b"))
	})

	It("completes directories only, as typed, under ~, relative and absolute paths", func() {
		Expect(dirSuggestions("~/p", home, cwd)).To(Equal([]string{"~/photos/", "~/projects/"}))
		Expect(dirSuggestions("ap", home, cwd)).To(Equal([]string{"app/", "apple/"}))
		Expect(dirSuggestions("", home, cwd)).To(Equal([]string{"app/", "apple/"}))
		Expect(dirSuggestions(home+"/proj", home, cwd)).To(Equal([]string{home + "/projects/"}))
	})

	It("hides dot directories until a dot is typed, and offers nothing for unreadable or missing ones", func() {
		Expect(dirSuggestions("~/", home, cwd)).NotTo(ContainElement("~/.config/"))
		Expect(dirSuggestions("~/.c", home, cwd)).To(Equal([]string{"~/.config/"}))
		Expect(dirSuggestions("nope/x", home, cwd)).To(BeEmpty())
		Expect(dirSuggestions("~", home, cwd)).To(BeEmpty())
	})
})
