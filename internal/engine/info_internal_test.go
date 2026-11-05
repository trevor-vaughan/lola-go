package engine

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

var _ = Describe("first lines of a hook", func() {
	It("a read that fails is an error, never the script's end", func() {
		testenv.Isolate()
		broken := io.MultiReader(strings.NewReader("#!/bin/sh\necho"), iotest{errors.New("read failed")})
		_, err := readFirstLines(broken)
		Expect(err).To(MatchError("read failed"))
	})
})

// iotest is a reader that fails with err.
type iotest struct{ err error }

func (r iotest) Read([]byte) (int, error) { return 0, r.err }

var _ = Describe("lola info's mentions", func() {
	It("degrade to a warn notice, without the file's path, when the scan fails", func() {
		tmp := testenv.Isolate()
		v := store.Version{Name: "pack", ContentDir: filepath.Join(tmp, "gone"), Module: &module.Module{Skills: []module.Skill{
			{Name: "alpha-one", Dir: "skills/alpha-one"}, {Name: "alpha-two", Dir: "skills/alpha-two"}}}}
		_, ms, notices := infoMentions(v)
		Expect(ms).To(Equal([]ComponentMentions{}))
		Expect(notices).To(HaveLen(1))
		Expect(notices[0].Level).To(Equal(LevelWarn))
		Expect(notices[0].Text).To(And(HavePrefix("pack: cannot read what its components mention: "), Not(ContainSubstring(tmp))))
	})
})

var _ = Describe("lola info's component mentions", func() {
	It("key by the full name: two long names that share a truncated prefix keep their own mentions", func() {
		tmp := testenv.Isolate()
		prefix := strings.Repeat("a", untrusted.MaxName)
		for rel, body := range map[string]string{"skills/one/SKILL.md": "Nothing here.\n", "skills/two/SKILL.md": "See zzzz.\n",
			"skills/zzzz/SKILL.md": "Alone.\n"} {
			Expect(os.MkdirAll(filepath.Dir(filepath.Join(tmp, rel)), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(tmp, rel), []byte(body), 0o644)).To(Succeed())
		}
		v := store.Version{Name: "pack", ContentDir: tmp, Module: &module.Module{Skills: []module.Skill{
			{Name: prefix + "-one", Dir: "skills/one"}, {Name: prefix + "-two", Dir: "skills/two"}, {Name: "zzzz", Dir: "skills/zzzz"}}}}
		ms, _, _ := infoMentions(v)
		states, err := componentStates(v, config.Exclusions{}, true, nil, false, func(string) (string, error) { return "", nil }, ms)
		Expect(err).NotTo(HaveOccurred())
		Expect(states).To(HaveLen(3))
		Expect(states[0].Mentions).To(BeEmpty(), "the first long name mentions nothing")
		Expect(states[1].Mentions).To(Equal([]string{"skill/zzzz"}))
	})
})
