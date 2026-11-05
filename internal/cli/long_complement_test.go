package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// Prof-P2-g: the complement of a non-exact --only is long; it is written as a block list and
// shown as the picks it leaves.
var _ = Describe("Prof-P2-g long complements", func() {
	var (
		p        paths.Paths
		tmp, big string
		goYml    string
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		big = filepath.Join(tmp, "big")
		files := map[string]string{"lola.yml": "name: big\n"}
		for i := 1; i <= 40; i++ {
			files[fmt.Sprintf("skills/s%02d/SKILL.md", i)] = fmt.Sprintf("---\ndescription: S%d\n---\nS\n", i)
		}
		writeTree(big, files)
		Expect(lola("install", big, "--user", "-t", "claude-code", "-y").code).To(Equal(0))
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		goYml = filepath.Join(p.UserProfiles, "go.yml")
	})

	It("writes the complement of a non-exact --only as a block list, and shows the picks", func() {
		r := lola("profile", "go", "--edit", "big", "--only", "skill/s01,skill/s02", "--override", "--user", "-y")
		Expect(r.code).To(Equal(0), r.err)
		b, err := os.ReadFile(goYml)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(MatchRegexp(`(?m)^    skip:\n      - skill/s03\n      - skill/s04\n`), "a block list, one skip a line")
		Expect(string(b)).NotTo(ContainSubstring("skip: ["))

		r = lola("profile", "go", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("only (not exact): skill/s01, skill/s02; 38 others skipped"))
		Expect(r.out).NotTo(ContainSubstring("skill/s03"))

		r = lola("profile", "go", "--user", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var v struct {
			Profiles []struct {
				Modules []struct {
					Only, Skip []string
				}
			}
		}
		Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed(), r.out)
		Expect(v.Profiles).NotTo(BeEmpty())
		var found bool
		for _, st := range v.Profiles {
			for _, m := range st.Modules {
				if len(m.Skip) == 38 && len(m.Only) == 0 {
					found = true
				}
			}
		}
		Expect(found).To(BeTrue(), "JSON still lists the 38 skips and no only: "+r.out)
	})

	It("shows a hand-written skip list as skips when it is not a small set of picks", func() {
		var items []string
		for i := 1; i <= 12; i++ {
			items = append(items, fmt.Sprintf("skill/s%02d", i))
		}
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nmodules:\n  - {name: big, source: " + big + ", skip: [" + strings.Join(items, ", ") + "]}\n"})
		r := lola("profile", "go", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("only (not exact)"))
		Expect(r.out).To(ContainSubstring("skip: skill/s01, skill/s02"))
	})

	It("keeps a short skip list a flow list and shown as skips", func() {
		r := lola("profile", "go", "--edit", "big", "--skip", "skill/s01", "--override", "--user", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(os.ReadFile(goYml)).To(ContainSubstring("skip: [skill/s01]\n"))
		r = lola("profile", "go", "--user")
		Expect(r.out).To(ContainSubstring("skip: skill/s01"))
		Expect(r.out).NotTo(ContainSubstring("only (not exact)"))
	})

	It("writes selections.yaml's long skip as a block list and words the install line as the picks", func() {
		proj := filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		r := lola("install", big, "-C", proj, "-t", "claude-code", "--only", "skill/s01,skill/s02", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("big: only (not exact) in selections.yaml: skill/s01, skill/s02; 38 others skipped"))
		b, err := os.ReadFile(filepath.Join(filepath.Dir(p.Selections), "selections.yaml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(regexp.MustCompile(`(?m)^\s+skip:\n\s+- skill/s03\n`).Match(b)).To(BeTrue(), string(b))
	})
})
