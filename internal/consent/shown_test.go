package consent_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/consent"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Shown", func() {
	var path string
	var s *consent.Shown
	key := func(c string) string { return strings.Repeat(c, 64) }
	BeforeEach(func() {
		path = filepath.Join(testenv.Isolate(), "state", "lola", "shown.json")
		s = consent.NewShown(path, filepath.Join(filepath.Dir(path), "locks", "shown.lock"))
	})

	It("holds nothing while the file is missing", func() {
		Expect(s.Holds(key("a"))).To(BeFalse())
		Expect(path).NotTo(BeAnExistingFile(), "reading writes nothing")
	})

	It("holds keys added, each one, until all of a set are added", func() {
		Expect(s.Add(key("a"))).To(Succeed())
		Expect(s.Holds(key("a"))).To(BeTrue())
		Expect(s.Holds(key("a"), key("b"))).To(BeFalse(), "one key of the set never shown")
		Expect(s.Add(key("b"), key("a"))).To(Succeed())
		Expect(s.Holds(key("a"), key("b"))).To(BeTrue())
		Expect(s.Holds()).To(BeTrue(), "an empty set is held")
		fi, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		Expect(os.ReadFile(path)).To(MatchJSON(`{"version":1,"keys":["` + key("a") + `","` + key("b") + `"]}`))
	})

	It("writes nothing when every key added is held already", func() {
		Expect(s.Add(key("a"))).To(Succeed())
		past := time.Now().Add(-time.Hour).Truncate(time.Second)
		Expect(os.Chtimes(path, past, past)).To(Succeed())
		Expect(s.Add(key("a"))).To(Succeed())
		Expect(s.Add()).To(Succeed())
		fi, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.ModTime()).To(BeTemporally("==", past))
	})

	It("refuses a corrupt, malformed or newer record, never overwrites it, and says it can be deleted", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		for body, want := range map[string]string{
			`{`:                                  "shown.json",
			`{"version":1,"keys":[],"extra":1}`:  "extra",
			`{"version":1,"keys":["not-a-key"]}`: "not-a-key",
			`{"version":1,"keys":["` + strings.Repeat("A", 64) + `"]}`: "AAAA",
			`{"version":9,"keys":[]}`:                                  "newer lola",
			`{"keys":[]}`:                                              "version 0",
			`{"version":1,"keys":[]} {}`:                               "after the record",
			`{"version":1,"keys":[]}}]`:                                "after the record",
			`{"version":1,"keys":["` + strings.Repeat("a", 64) + `","` + strings.Repeat("a", 64) + `"]}`: "twice",
		} {
			Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
			_, err := s.Holds(key("a"))
			Expect(err).To(MatchError(ContainSubstring(want)), body)
			if want != "newer lola" { // a newer lola's record is kept for it: upgrade
				Expect(errs.Hints(err)).To(ContainElement(And(ContainSubstring("rm "), ContainSubstring(path), ContainSubstring("shows you"))), body)
			}
			Expect(s.Add(key("a"))).To(MatchError(ContainSubstring(want)), body)
			Expect(os.ReadFile(path)).To(Equal([]byte(body)))
		}
	})

	It("keeps every key of adds made at once", func() {
		var wg sync.WaitGroup
		for _, c := range "0123456789abcdef" {
			wg.Go(func() { Expect(s.Add(key(string(c)))).To(Succeed()) })
		}
		wg.Wait()
		for _, c := range "0123456789abcdef" {
			Expect(s.Holds(key(string(c)))).To(BeTrue(), string(c))
		}
	})

	It("refuses to add a key that is no sha256", func() {
		Expect(s.Add("nope")).To(MatchError(ContainSubstring("nope")))
		Expect(path).NotTo(BeAnExistingFile())
	})
})
