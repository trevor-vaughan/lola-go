package market_test

import (
	"maps"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

const knownCommit = "0123456789abcdef0123456789abcdef01234567"

// knownSeed is a seed of one entry whose lines are replaced by set, a field name to its YAML line
// ("# none" leaves the field out).
func knownSeed(set map[string]string) []byte {
	fields := map[string]string{
		"id":      "id: acme",
		"source":  "source: https://example.com/acme/market.git",
		"path":    "path: catalog",
		"commit":  "commit: " + knownCommit,
		"curated": "curated: 2026-11-22",
		"reason":  "reason: Acme's tools, reviewed by the maintainers",
	}
	maps.Copy(fields, set)
	var b strings.Builder
	b.WriteString("version: 1\nmarkets:\n")
	prefix := "  - "
	for _, k := range []string{"id", "source", "path", "commit", "curated", "reason", "extra"} {
		if fields[k] != "" && fields[k] != "# none" {
			b.WriteString(prefix + fields[k] + "\n")
			prefix = "    "
		}
	}
	return []byte(b.String())
}

var _ = Describe("known marketplaces", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("loads a valid seed, path optional", func() {
		got, err := market.LoadKnown(knownSeed(nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]market.KnownMarket{{ID: "acme", Source: "https://example.com/acme/market.git", Path: "catalog",
			Commit: knownCommit, Curated: "2026-11-22", Reason: "Acme's tools, reviewed by the maintainers"}}))
		got, err = market.LoadKnown(knownSeed(map[string]string{"path": "# none"}))
		Expect(err).NotTo(HaveOccurred())
		Expect(got[0].Path).To(BeEmpty())
	})

	It("loads a reason of exactly 512 bytes", func() {
		got, err := market.LoadKnown(knownSeed(map[string]string{"reason": "reason: " + strings.Repeat("x", 512)}))
		Expect(err).NotTo(HaveOccurred())
		Expect(got[0].Reason).To(HaveLen(512))
	})

	It("loads an empty list as none, never nil", func() {
		got, err := market.LoadKnown([]byte("version: 1\nmarkets: []\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeEmpty())
		Expect(got).NotTo(BeNil())
	})

	It("parses the seed lola ships", func() {
		got, err := market.Known()
		Expect(err).NotTo(HaveOccurred())
		Expect(got).NotTo(BeNil())
	})

	DescribeTable("refuses a seed entry that is not sound, naming its index and field",
		func(set map[string]string, msg string) {
			_, err := market.LoadKnown(knownSeed(set))
			Expect(err).To(MatchError(ContainSubstring(msg)))
		},
		Entry("a bad id", map[string]string{"id": "id: Not A Name"}, "markets[0].id"),
		Entry("a reserved id", map[string]string{"id": "id: add"}, "markets[0].id"),
		Entry("no id", map[string]string{"id": "# none"}, "markets[0].id"),
		Entry("a commit that is a branch", map[string]string{"commit": "commit: main"}, "markets[0].commit"),
		Entry("a short commit", map[string]string{"commit": "commit: 0123456"}, "markets[0].commit"),
		Entry("an uppercase commit", map[string]string{"commit": "commit: " + strings.ToUpper(knownCommit)}, "markets[0].commit"),
		Entry("no commit", map[string]string{"commit": "# none"}, "markets[0].commit"),
		Entry("a plain http source", map[string]string{"source": "source: http://example.com/a.git"}, "markets[0].source"),
		Entry("an ssh source", map[string]string{"source": "source: git@example.com:a/b.git"}, "markets[0].source"),
		Entry("a folder source", map[string]string{"source": "source: /srv/market"}, "markets[0].source"),
		Entry("a catalog file source", map[string]string{"source": "source: https://example.com/catalog.json"}, "markets[0].source"),
		Entry("a source with credentials", map[string]string{"source": "source: https://user@example.com/a.git"}, "markets[0].source"),
		Entry("no source", map[string]string{"source": "# none"}, "markets[0].source"),
		Entry("a source with an invisible character", map[string]string{"source": `source: "https://example.com/a\u200b.git"`}, "invisible characters"),
		Entry("a source over the recorded limit", map[string]string{"source": "source: https://example.com/" + strings.Repeat("a", 4096) + ".git"}, "longer than 4096 bytes"),
		Entry("a path that leaves the repository", map[string]string{"path": "path: ../x"}, "markets[0].path"),
		Entry("an absolute path", map[string]string{"path": "path: /x"}, "markets[0].path"),
		Entry("a curated date that is not a date", map[string]string{"curated": "curated: yesterday"}, "markets[0].curated"),
		Entry("a curated date with a time", map[string]string{"curated": "curated: 2026-11-22T10:00:00Z"}, "markets[0].curated"),
		Entry("no curated date", map[string]string{"curated": "# none"}, "markets[0].curated"),
		Entry("an empty reason", map[string]string{"reason": `reason: ""`}, "markets[0].reason"),
		Entry("no reason", map[string]string{"reason": "# none"}, "markets[0].reason"),
		Entry("a reason over 512 bytes", map[string]string{"reason": "reason: " + strings.Repeat("x", 513)}, "markets[0].reason"),
		Entry("a reason of several lines", map[string]string{"reason": `reason: "one\ntwo"`}, "markets[0].reason"),
		Entry("a reason with a control character", map[string]string{"reason": `reason: "red \x1b[31m"`}, "markets[0].reason"),
		Entry("an unknown key", map[string]string{"extra": "homepage: https://example.com"}, "homepage"),
	)

	It("names the index of the entry that is not sound", func() {
		two := string(knownSeed(nil)) + "  - id: other\n    source: https://example.com/o.git\n    commit: nope\n    curated: 2026-11-22\n    reason: r\n"
		_, err := market.LoadKnown([]byte(two))
		Expect(err).To(MatchError(ContainSubstring("markets[1].commit")))
	})

	It("refuses a duplicate id", func() {
		one := string(knownSeed(nil))
		_, err := market.LoadKnown([]byte(one + strings.TrimPrefix(one, "version: 1\nmarkets:\n")))
		Expect(err).To(MatchError(And(ContainSubstring("markets[1].id"), ContainSubstring("acme"))))
	})

	DescribeTable("refuses a seed that is not the schema",
		func(seed, msg string) {
			_, err := market.LoadKnown([]byte(seed))
			Expect(err).To(MatchError(ContainSubstring(msg)))
		},
		Entry("an unknown top-level key", "version: 1\nmarkets: []\nextra: 1\n", "extra"),
		Entry("another version", "version: 2\nmarkets: []\n", "version"),
		Entry("no version", "markets: []\n", "version"),
		Entry("two documents", "version: 1\nmarkets: []\n---\nversion: 1\n", "more than one"),
		Entry("an alias", "version: 1\nmarkets: &a []\n", "anchors"),
		Entry("not a mapping", "- a\n", "mapping"),
	)
})
