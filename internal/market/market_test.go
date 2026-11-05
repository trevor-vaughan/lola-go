package market_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// claude is a Claude marketplace.json whose plugins are the given JSON objects.
func claude(plugins ...string) []byte {
	return []byte(`{"name": "acme", "owner": {"name": "Acme"}, "plugins": [` + strings.Join(plugins, ",") + `]}`)
}

// parsed is the Claude catalog of plugins, which must load.
func parsed(plugins ...string) market.Catalog {
	GinkgoHelper()
	cat, err := market.Parse(market.FormatClaude, claude(plugins...))
	Expect(err).NotTo(HaveOccurred())
	return cat
}

// reasons maps each skipped entry's name to its reason.
func reasons(cat market.Catalog) map[string]string {
	out := map[string]string{}
	for _, s := range cat.Skipped {
		out[s.Name] = s.Reason
	}
	return out
}

// gitPin is the pin of a git marketplace whose catalog is Claude Code's file at its root.
func gitPin(commit string) market.Pin {
	return market.Pin{Version: 1, Source: "https://git.example.com/acme/plugins.git", Ref: "main", File: ".claude-plugin/marketplace.json",
		Commit: commit, Digest: "sha256:" + strings.Repeat("d", 64), Absent: []string{}}
}

var _ = Describe("catalogs", func() {
	var commit, other string
	BeforeEach(func() {
		testenv.Isolate()
		commit, other = strings.Repeat("a", 40), strings.Repeat("b", 40)
	})

	It("a Claude marketplace.json and a Python YAML catalog load into the same model", func() {
		cat, err := market.Parse(market.FormatClaude, []byte(`{
  "$schema": "https://anthropic.com/claude-code/marketplace.schema.json",
  "name": "acme", "owner": {"name": "Acme Tools", "email": "tools@acme.example"},
  "metadata": {"description": "Acme's tools", "version": "2.0.0", "pluginRoot": "./plugins"},
  "plugins": [
    {"name": "formatter", "source": {"source": "git-subdir", "url": "acme/monorepo", "path": "tools/formatter", "ref": "v2.0.0", "sha": "`+commit+`"},
     "description": "Formats code", "version": "2.0.0", "tags": ["format"], "keywords": ["style"], "category": "quality", "strict": false},
    {"name": "linter", "source": {"source": "github", "repo": "acme/linter", "ref": "main"}},
    {"name": "review", "source": {"source": "url", "url": "https://git.example.com/review.git", "ref": "`+other+`"}},
    {"name": "bundle", "source": {"source": "archive", "url": "https://dl.example.com/bundle.tar.gz", "sha256": "`+strings.Repeat("AB", 32)+`"}},
    {"name": "local", "source": "./tools/local", "dependencies": ["linter", "lint-core@other"]},
    {"name": "bare", "source": "bare"}
  ]}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(cat.Format).To(Equal(market.FormatClaude))
		Expect([]string{cat.Name, cat.Owner, cat.Description, cat.Version}).To(Equal([]string{"acme", "Acme Tools", "Acme's tools", "2.0.0"}))
		Expect(cat.Skipped).To(BeEmpty())
		byName := map[string]market.Entry{}
		for _, e := range cat.Entries {
			byName[e.Name] = e
		}
		Expect(byName["formatter"].Source).To(Equal(manifest.Entry{Name: "formatter", Source: "https://github.com/acme/monorepo.git",
			Path: "tools/formatter", Ref: "v2.0.0", Commit: commit}))
		Expect(byName["formatter"].Tags).To(Equal([]string{"format", "style", "quality"}))
		Expect([]string{byName["formatter"].Description, byName["formatter"].Version, byName["formatter"].Kind}).To(Equal([]string{"Formats code", "2.0.0", "git"}))
		Expect(byName["linter"].Source).To(Equal(manifest.Entry{Name: "linter", Source: "https://github.com/acme/linter.git", Ref: "main"}))
		Expect(byName["review"].Source).To(Equal(manifest.Entry{Name: "review", Source: "https://git.example.com/review.git", Commit: other}),
			"a full-commit ref is a commit pin, checked against the default branch")
		Expect(byName["bundle"].Source).To(Equal(manifest.Entry{Name: "bundle", Source: "https://dl.example.com/bundle.tar.gz"}))
		Expect(byName["bundle"].Kind).To(Equal("archive"))
		Expect(byName["bundle"].ArchiveSHA256).To(Equal(strings.Repeat("ab", 32)))
		Expect(byName["local"].Relative).To(Equal("tools/local"))
		Expect(byName["local"].Notices).To(Equal([]string{"lists dependencies lola does not install: linter, lint-core@other"}))
		Expect(byName["bare"].Relative).To(Equal("plugins/bare"))

		bound := market.Bind(cat, gitPin(commit))
		Expect(bound.Skipped).To(BeEmpty())
		for _, e := range bound.Entries {
			byName[e.Name] = e
		}
		Expect(byName["local"].Source).To(Equal(manifest.Entry{Name: "local", Source: "https://git.example.com/acme/plugins.git", Ref: "main",
			Path: "tools/local", Commit: commit}), "a relative entry pins the marketplace's own commit")
		Expect(byName["bare"].Source.Path).To(Equal("plugins/bare"))

		yml, err := market.Parse(market.FormatYAML, []byte(`name: Lola General Marketplace
description: General modules
version: 1.0.0
modules:
  - name: formatter
    description: Formats code
    version: 2.0.0
    repository: https://github.com/acme/monorepo.git
    path: tools/formatter
    ref: v2.0.0
    tags: [format, style]
  - name: pinned
    repository: https://git.example.com/review.git
    ref: `+other+`
  - name: bundle
    repository: https://dl.example.com/bundle.tar.gz
  - name: hooked
    repository: https://github.com/acme/hooked.git
    hooks:
      pre-install: scripts/pre.sh
`))
		Expect(err).NotTo(HaveOccurred())
		Expect(yml.Format).To(Equal(market.FormatYAML))
		Expect([]string{yml.Name, yml.Description, yml.Version}).To(Equal([]string{"Lola General Marketplace", "General modules", "1.0.0"}))
		Expect(yml.Skipped).To(BeEmpty())
		Expect(yml.Entries[0].Source).To(Equal(manifest.Entry{Name: "formatter", Source: "https://github.com/acme/monorepo.git",
			Path: "tools/formatter", Ref: "v2.0.0"}), "the same module, the same model")
		Expect(yml.Entries[0].Tags).To(Equal([]string{"format", "style"}))
		Expect(yml.Entries[1].Source).To(Equal(manifest.Entry{Name: "pinned", Source: "https://git.example.com/review.git", Commit: other}))
		Expect(yml.Entries[2].Source).To(Equal(manifest.Entry{Name: "bundle", Source: "https://dl.example.com/bundle.tar.gz"}))
		Expect(yml.Entries[2].Kind).To(Equal("archive"), "an https archive by its URL's suffix")
		Expect(yml.Entries[3].Notices).To(Equal([]string{"catalog install scripts are ignored"}))
	})

	It("an entry with an unsupported source kind is skipped with its reason and the rest load", func() {
		missing := parsed(`{"name": "nourl", "source": {"source": "url"}}`, `{"name": "norepo", "source": {"source": "github"}}`,
			`{"name": "emptyurl", "source": {"source": "archive", "url": ""}}`,
			`{"name": "nosub", "source": {"source": "git-subdir", "path": "tools/x"}}`)
		Expect(reasons(missing)).To(Equal(map[string]string{"nourl": "source.url is missing", "norepo": "source.repo is missing",
			"emptyurl": "source.url is missing", "nosub": "source.url is missing"}))

		cat := parsed(
			`{"name": "ok", "source": {"source": "github", "repo": "acme/ok"}}`,
			`{"name": "n", "source": {"source": "npm", "package": "@acme/n"}}`,
			`{"name": "c", "source": {"source": "command", "command": "touch /tmp/x"}}`,
			`{"name": "w", "source": {"source": "weird"}}`,
			`{"name": "k", "source": "./k", "mcpServers": {"x": {"command": "x"}}}`,
			`{"name": "l", "source": "./l", "lspServers": {}}`,
			`{"name": "h", "source": "./h", "headers": {"Authorization": "Bearer x"}}`,
			`{"name": "hh", "source": "./hh", "headersHelper": "echo token"}`,
			`{"name": "s1", "source": {"source": "url", "url": "ssh://git@example.com/s1.git"}}`,
			`{"name": "s2", "source": {"source": "url", "url": "git@example.com:acme/s2.git"}}`,
		)
		Expect(cat.Entries).To(HaveLen(1))
		Expect(cat.Entries[0].Name).To(Equal("ok"))
		Expect(reasons(cat)).To(Equal(map[string]string{
			"n":  "npm sources are not supported: installing one runs a package manager",
			"c":  "command sources run a program; lola never does",
			"w":  "weird sources are not supported",
			"k":  "the catalog declares its mcpServers; lola reads a plugin's components from its own files, never from the catalog",
			"l":  "the catalog declares its lspServers; lola reads a plugin's components from its own files, never from the catalog",
			"h":  "it carries headers, credentials lola never sends",
			"hh": "its headersHelper runs a program; lola never does",
			"s1": "ssh sources in a catalog are not supported: they connect to a stranger's host with your ssh keys",
			"s2": "ssh sources in a catalog are not supported: they connect to a stranger's host with your ssh keys",
		}))

		yml, err := market.Parse(market.FormatYAML, []byte("name: m\nmodules:\n  - {name: ok, repository: \"https://github.com/acme/ok.git\"}\n"+
			"  - {name: s, repository: \"git@github.com:acme/s.git\"}\n  - {name: norepo}\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(yml.Entries).To(HaveLen(1))
		Expect(reasons(yml)).To(Equal(map[string]string{
			"s":      "ssh sources in a catalog are not supported: they connect to a stranger's host with your ssh keys",
			"norepo": "the module has no repository",
		}))
	})

	It("a crafted catalog refuses at load", func() {
		deep := strings.Repeat("[", 64) + strings.Repeat("]", 64)
		// Exactly 64 levels, the root object counted, load.
		_, err := market.Parse(market.FormatClaude, []byte(`{"name": "acme", "owner": {"name": "A"}, "plugins": [], "x": `+
			strings.Repeat("[", 63)+strings.Repeat("]", 63)+`}`))
		Expect(err).NotTo(HaveOccurred())
		// The byte cap does not bound what decoding allocates: a value budget does, for JSON and YAML.
		_, err = market.Parse(market.FormatClaude, []byte(`{"name": "acme", "owner": {"name": "A"}, "plugins": [], "pad": [`+
			strings.TrimSuffix(strings.Repeat("0,", 1<<18), ",")+`]}`))
		Expect(err).To(MatchError("holds more than 262144 JSON values"))
		_, err = market.Parse(market.FormatYAML, []byte("name: m\nmodules: []\npad: ["+strings.TrimSuffix(strings.Repeat("0,", 1<<18), ",")+"]\n"))
		Expect(err).To(MatchError("is larger than 524288 bytes, the most lola reads of a YAML catalog"))
		_, err = market.Parse(market.FormatYAML, []byte("name: m\nmodules: []\npad: ["+strings.TrimSuffix(strings.Repeat("0,", 200000), ",")+"]\n"))
		Expect(err).To(MatchError("holds more than 131072 YAML nodes"))
		_, err = market.Parse(market.FormatClaude, claude(`{"name": "x", "source": {"source": "url", "url": "https://e.example/x.git", "`+
			strings.Repeat("k", 300)+`": "\u0007"}}`))
		Expect(err).To(MatchError(And(HavePrefix("plugins[0].source.kkk"), ContainSubstring("… holds control or invisible characters"))),
			"a field named after a long key is cut")
		var many []string
		for i := range 4097 {
			many = append(many, fmt.Sprintf(`{"name": "e%d", "source": "./e"}`, i))
		}
		for in, want := range map[string]string{
			string(claude(`{"name": "x", "source": "./x", "description": "` + strings.Repeat("a", 8<<20) + `"}`)): "is larger than 8388608 bytes",
			string(claude(many...)): "lists more than 4096 plugins",
			`{"name": "acme", "owner": {"name": "A"}, "plugins": [], "x": ` + deep + `}`:                        "nests deeper than 64 levels",
			string(claude(`{"name": "x", "source": "./x", "description": "a\u0007b"}`)):                         "plugins[0].description holds control or invisible characters",
			string(claude(`{"name": "x", "source": "./x", "description": "a\u200bb"}`)):                         "plugins[0].description holds control or invisible characters",
			string(claude(`{"name": "x", "source": {"source": "url", "url": "https://x.example/\u202e.git"}}`)): "plugins[0].source.url holds control or invisible characters",
			string(claude(`{"name": "x", "source": "./x", "ta\u0000gs": []}`)):                                  "a key of plugins[0] holds control or invisible characters",
			`{"name": "ac\u001bme", "owner": {"name": "A"}, "plugins": []}`:                                     "name holds control or invisible characters",
			`{"name": "acme", "owner": {"name": "A"}, "plugins": [], "pa\u0000d": 1}`:                           "a key of the catalog holds control or invisible characters",
			`{"name": "acme", "owner": {"name": "A\nB"}, "plugins": []}`:                                        "owner.name holds a line break; catalog text is one line",
			`{"name": "acme", "owner": {"name": "A\rB"}, "plugins": []}`:                                        "owner.name holds a line break; catalog text is one line",
			`{"name": "acme", "owner": {"name": "A"}, "plugins": [], "p\nad": 1}`:                               "a key of the catalog holds a line break; catalog text is one line",
			`[]`: "is not a JSON object",
			`{"name": "` + strings.Repeat("n", 257) + `", "owner": {"name": "A"}, "plugins": []}`:                      "name is longer than 256 bytes",
			`{"name": "a", "owner": {"name": "` + strings.Repeat("o", 257) + `"}, "plugins": []}`:                      "owner.name is longer than 256 bytes",
			`{"name": "a", "version": "` + strings.Repeat("1", 257) + `", "owner": {"name": "A"}, "plugins": []}`:      "version is longer than 256 bytes",
			`{"name": "a", "description": "` + strings.Repeat("d", 1025) + `", "owner": {"name": "A"}, "plugins": []}`: "description is longer than 1024 bytes",
			`{"owner": {"name": "A"}, "plugins": []}`:                                                                  "has no name",
			`{"name": "acme", "owner": {"name": "A"}}`:                                                                 "has no plugins",
			`{"name": "acme", "owner": {"name": "A"}, "plugins": {}}`:                                                  "plugins is not a list",
			`{"name": "acme", "owner": {"name": "A"}, "plugins": [], "name": "b"}`:                                     "the key name is ambiguous",
			`{"name": "acme", "owner": {"name": "A"}, "Plugins": [], "plugins": []}`:                                   "the key Plugins is ambiguous",
			`{"name": "acme", "owner": {"name": "A", "NAME": "B"}, "plugins": []}`:                                     "the key owner.NAME is ambiguous",
			`{"name": "acme", "owner": {"name": "A"}, "plugins": []} {}`:                                               "holds data after its JSON value",
			`{"name": "acme", "owner": {"name": "A"}, "plugins": []}]`:                                                 "holds data after its JSON value",
			"{\"name\": \"a\xff\", \"owner\": {\"name\": \"A\"}, \"plugins\": []}":                                     "holds bytes that are not UTF-8",
		} {
			_, err := market.Parse(market.FormatClaude, []byte(in))
			Expect(err).To(MatchError(ContainSubstring(want)), want)
		}
		for in, want := range map[string]string{
			"name: m\nmodules:\n  - &a {name: x, repository: \"https://e.example/x.git\"}\n  - *a\n":             "anchors, aliases and merge keys (<<) are not supported",
			"name: m\nbase: {a: 1}\nmodules:\n  - <<: {name: x}\n":                                               "anchors, aliases and merge keys (<<) are not supported",
			"name: m\nmodules: []\n---\nname: n\n":                                                               "holds more than one YAML document",
			"name: m\nname: n\nmodules: []\n":                                                                    "the key name is given twice",
			"name: \"m\\u0007\"\nmodules: []\n":                                                                  "line 1: name holds control or invisible characters",
			"name: m\ndescription: >\n  General\n  modules\nmodules: []\n":                                       "line 2: description holds a line break; catalog text is one line, so use a plain or quoted scalar",
			"name: m\n\"a\\nb\": 1\nmodules: []\n":                                                               "line 2: a key of the catalog holds a line break; catalog text is one line",
			"name: m\nmodules:\n  - {name: x, repository: \"https://e.example/x.git\", tags: [\"a\\u200eb\"]}\n": "line 3: modules[0].tags holds control or invisible characters",
			"- a\n":                      "is not a YAML mapping",
			"name: m\n":                  "has no modules",
			"modules: []\n":              "has no name",
			"name: m\nmodules: {a: 1}\n": "modules is not a list",
		} {
			_, err := market.Parse(market.FormatYAML, []byte(in))
			Expect(err).To(MatchError(ContainSubstring(want)), in)
		}

		cat := parsed(`{"name": "dup", "source": "./a", "source": "./b"}`, `{"name": "case", "source": "./a", "Source": "./b"}`,
			`{"name": "inner", "source": {"source": "url", "url": "https://e.example/x.git", "URL": "https://f.example/y.git"}}`,
			`{"name": "long", "source": "./long", "description": "`+strings.Repeat("a", 4097)+`"}`,
			`{"name": "wordy", "source": "./wordy", "description": "`+strings.Repeat("a", 4096)+`"}`,
			`{"name": "src", "source": {"source": "url", "url": "https://e.example/`+strings.Repeat("a", 4096)+`.git"}}`,
			`{"name": "tags", "source": "./t", "tags": [`+strings.TrimSuffix(strings.Repeat(`"t",`, 65), ",")+`]}`,
			`{"name": "tag", "source": "./t", "tags": ["`+strings.Repeat("t", 65)+`"]}`,
			`{"name": "ver", "source": "./v", "version": "`+strings.Repeat("1", 257)+`"}`,
			`{"name": "fine", "source": "./fine", "Strict": true, "homepage": 3}`)
		Expect(cat.Entries).To(HaveLen(2), "a case variant of an ignored key is no ambiguity, and 4096 bytes of description load")
		Expect(cat.Skipped).To(ConsistOf(
			market.Skip{Name: "dup", Reason: "ambiguous key source", Kind: "ambiguous entry"},
			market.Skip{Name: "case", Reason: "ambiguous key Source", Kind: "ambiguous entry"},
			market.Skip{Name: "inner", Reason: "ambiguous key source.URL", Kind: "ambiguous entry"},
			market.Skip{Name: "long", Reason: "description is longer than 4096 bytes", Kind: "oversized entry"},
			market.Skip{Name: "src", Reason: "source.url is longer than 4096 bytes", Kind: "oversized entry"},
			market.Skip{Name: "tags", Reason: "it has more than 64 tags, keywords and categories", Kind: "oversized entry"},
			market.Skip{Name: "tag", Reason: "a tag is longer than 64 bytes", Kind: "oversized entry"},
			market.Skip{Name: "ver", Reason: "version is longer than 256 bytes", Kind: "oversized entry"},
		))
		yml, err := market.Parse(market.FormatYAML, []byte("name: m\nmodules:\n"+
			"  - {name: wordy, repository: \"https://e.example/w.git\", description: "+strings.Repeat("a", 4096)+"}\n"+
			"  - {name: long, repository: \"https://e.example/l.git\", description: "+strings.Repeat("a", 4097)+"}\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(yml.Entries).To(ConsistOf(HaveField("Name", "wordy")), "an entry's description may hold 4096 bytes in either format")
		Expect(yml.Skipped).To(Equal([]market.Skip{{Name: "long", Reason: "description is longer than 4096 bytes", Kind: "oversized entry"}}))
	})

	It("a catalog entry naming a local, http, localhost or private-address source is skipped", func() {
		dir, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		mod := filepath.Join(dir, "mods", "x")
		Expect(os.MkdirAll(filepath.Join(mod, "skills", "s"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(mod, "skills", "s", "SKILL.md"), []byte("---\nname: s\n---\n"), 0o644)).To(Succeed())
		GinkgoT().Chdir(dir)
		jsonStr := func(s string) string { b, _ := json.Marshal(s); return string(b) }
		urls := map[string]string{
			mod:                         "is on this machine; a catalog entry names only git repositories and https archives",
			"file://" + mod:             "is on this machine",
			"mods/x":                    "is on this machine",
			"http://example.com/x.git":  "uses http://; a catalog entry's sources must use https://",
			"https://localhost/x.git":   "names localhost, this machine",
			"https://127.0.0.1/x.git":   "names the loopback address 127.0.0.1",
			"https://10.1.2.3/x.git":    "names the private address 10.1.2.3",
			"https://0x7f.1/x.git":      "names the host 0x7f.1, which git and ssh read as an IP address",
			"https://169.254.169.254/x": "names the link-local address 169.254.169.254",
		}
		var plugins []string
		var yml strings.Builder
		yml.WriteString("name: m\nmodules:\n")
		i := 0
		names := map[string]string{}
		for u, want := range urls {
			name := fmt.Sprintf("e%d", i)
			names[name] = want
			plugins = append(plugins, `{"name": "`+name+`", "source": {"source": "url", "url": `+jsonStr(u)+`}}`)
			fmt.Fprintf(&yml, "  - {name: %s, repository: %s}\n", name, jsonStr(u))
			i++
		}
		plugins = append(plugins, `{"name": "gh", "source": {"source": "github", "repo": "../../etc"}}`)
		cat := parsed(plugins...)
		Expect(cat.Entries).To(BeEmpty(), "none maps to a folder source")
		got := reasons(cat)
		for name, want := range names {
			Expect(got[name]).To(ContainSubstring(want), name)
		}
		Expect(got["gh"]).To(Equal("source.repo ../../etc is not a GitHub owner/name"))

		ycat, err := market.Parse(market.FormatYAML, []byte(yml.String()))
		Expect(err).NotTo(HaveOccurred())
		Expect(ycat.Entries).To(BeEmpty())
		got = reasons(ycat)
		for name, want := range names {
			Expect(got[name]).To(ContainSubstring(want), name)
		}
	})

	It("a relative entry cannot leave the marketplace tree", func() {
		rooted, err := market.Parse(market.FormatClaude, []byte(`{"name": "a", "owner": {"name": "A"}, "metadata": {"pluginRoot": "./plugins"},
  "plugins": [{"name": "abs", "source": "/etc"}, {"name": "up", "source": "../x"}, {"name": "ok", "source": "fmt"}]}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(reasons(rooted)).To(Equal(map[string]string{"abs": "source /etc is absolute; it must be relative to the marketplace",
			"up": "source ../x leaves the marketplace (..)"}), "a bare name is checked before it is joined to pluginRoot")
		Expect(rooted.Entries[0].Relative).To(Equal("plugins/fmt"))

		cat := parsed(`{"name": "up", "source": "./../x"}`, `{"name": "abs", "source": "/etc"}`, `{"name": "bs", "source": ".\\x"}`,
			`{"name": "bare", "source": "x"}`, `{"name": "inner", "source": "./a/../../x"}`,
			`{"name": "sub", "source": {"source": "git-subdir", "url": "https://e.example/m.git", "path": "../x"}}`,
			`{"name": "subbs", "source": {"source": "git-subdir", "url": "https://e.example/m.git", "path": "a\\b"}}`,
			`{"name": "ok", "source": "./tools/ok"}`, `{"name": "gone", "source": "./tools/gone"}`, `{"name": "root", "source": "."}`)
		Expect(reasons(cat)).To(Equal(map[string]string{
			"up":    "source ./../x leaves the marketplace (..)",
			"abs":   "relative source /etc does not start with ./ (a bare name needs metadata.pluginRoot)",
			"bs":    "relative source .\\x does not start with ./ (a bare name needs metadata.pluginRoot)",
			"bare":  "relative source x does not start with ./ (a bare name needs metadata.pluginRoot)",
			"inner": "source ./a/../../x leaves the marketplace (..)",
			"sub":   "path ../x leaves the marketplace (..)",
			"subbs": "path a\\b holds a backslash; paths in a marketplace are slash-separated",
		}))
		p := gitPin(commit)
		Expect(market.RelativeDirs(cat, p.File)).To(Equal([]string{"tools/ok", "tools/gone", "."}))
		p.Absent = []string{"tools/gone"}
		bound := market.Bind(cat, p)
		Expect(reasons(bound)["gone"]).To(Equal("tools/gone is not in the marketplace"))
		Expect(bound.Entries).To(HaveLen(2))
		Expect(bound.Entries[1].Source.Path).To(Equal(""), "the marketplace's root is the source itself")

		p.File = "sub/marketplace.yml"
		Expect(market.RelativeDirs(cat, p.File)).To(Equal([]string{"sub/tools/ok", "sub/tools/gone", "sub"}))
		folder := gitPin("")
		folder.Source, folder.Ref = filepath.Join(testenv.Isolate(), "market"), ""
		bound = market.Bind(cat, folder)
		Expect(bound.Entries[0].Source).To(Equal(manifest.Entry{Name: "ok", Source: folder.Source, Path: "tools/ok"}), "a folder's entries are unpinned")
		Expect(bound.Entries[0].Kind).To(Equal("folder"))
		file := gitPin("")
		file.Source, file.File, file.Ref = "https://raw.example.com/acme/marketplace.json", "marketplace.json", ""
		bound = market.Bind(cat, file)
		Expect(bound.Entries).To(BeEmpty())
		Expect(reasons(bound)["ok"]).To(Equal("a relative source needs the marketplace's files, and this marketplace is a single catalog file"))

		for _, root := range []string{"../plugins", "/plugins", `plug\ins`} {
			_, err := market.Parse(market.FormatClaude, []byte(`{"name": "a", "owner": {"name": "A"}, "metadata": {"pluginRoot": `+
				strings.ReplaceAll(fmt.Sprintf("%q", root), `\\`, `\\`)+`}, "plugins": []}`))
			Expect(err).To(MatchError(HavePrefix("metadata.pluginRoot ")), root)
		}
	})

	It("nothing in a catalog runs a command", func() {
		cat := parsed(`{"name": "c", "source": {"source": "command", "command": "touch marker"}}`,
			`{"name": "hh", "source": "./hh", "headersHelper": "touch marker"}`)
		Expect(cat.Entries).To(BeEmpty())
		Expect(cat.Skipped).To(HaveLen(2))
		yml, err := market.Parse(market.FormatYAML, []byte("name: m\nmodules:\n  - name: hooked\n    repository: https://github.com/acme/h.git\n"+
			"    hooks: {pre-install: touch marker, post-install: touch marker}\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(yml.Entries).To(HaveLen(1), "the entry is kept; its scripts are never run")
		Expect(yml.Entries[0].Notices).To(Equal([]string{"catalog install scripts are ignored"}))
	})

	It("binds a relative entry under the pin's root, checking what the fetch did not find from the catalog's tree", func() {
		cat := parsed(`{"name": "plain", "source": "./plain"}`, `{"name": "top", "source": "./", "skills": ["./skills/a"]}`,
			`{"name": "gone", "source": "./gone"}`)
		pin := market.Pin{Version: 1, Source: "https://dl.example.com/m.tar.gz", File: ".claude-plugin/marketplace.json",
			Digest: "sha256:" + strings.Repeat("d", 64), Absent: []string{"gone"}, Root: "repo-abc"}
		bound := market.Bind(cat, pin)
		Expect(reasons(bound)).To(Equal(map[string]string{"gone": "gone is not in the marketplace"}))
		Expect(bound.Entries).To(HaveLen(2))
		Expect(bound.Entries[0].Source).To(Equal(manifest.Entry{Name: "plain", Source: pin.Source, Path: "repo-abc/plain"}))
		Expect(bound.Entries[1].Source).To(Equal(manifest.Entry{Name: "top", Source: pin.Source, Path: "repo-abc"}))
		Expect(bound.Entries[1].Components).To(Equal(&market.Components{Skills: []string{"skills/a"}}), "a selection stays relative to the entry")
		pin.Root = ""
		Expect(market.Bind(cat, pin).Entries[1].Source.Path).To(BeEmpty(), "a pin with no root binds at the source's top")
	})

	It("a catalog skills list selects the module's skills from the marketplace's own tree", func() {
		// Anthropic's anthropics/skills marketplace lists its plugins this way (user decision,
		// 2026-10-21, amending ruling R-6c-4).
		cat := parsed(`{"name": "document-skills", "source": "./", "strict": false, "skills": ["./skills/xlsx", "./skills/docx"]}`,
			`{"name": "tools", "source": "./plugins/tools", "commands": ["./commands/fmt.md", "./more"], "agents": "./agents/reviewer.md"}`,
			`{"name": "up", "source": "./", "skills": ["./skills/../../x"]}`,
			`{"name": "abs", "source": "./", "skills": ["/etc"]}`,
			`{"name": "back", "source": "./", "agents": ["agents\\a.md"]}`,
			`{"name": "root", "source": "./", "skills": ["./"]}`,
			`{"name": "twice", "source": "./", "skills": ["./a/x", "./b/x"]}`,
			`{"name": "none", "source": "./", "skills": []}`,
			`{"name": "notlist", "source": "./", "skills": {"x": 1}}`,
			`{"name": "hooked", "source": "./", "skills": ["./skills/x"], "hooks": "./hooks.json"}`)
		Expect(reasons(cat)).To(Equal(map[string]string{
			"up":      "skills path ./skills/../../x is not a relative path inside the plugin's directory",
			"abs":     "skills path /etc is not a relative path inside the plugin's directory",
			"back":    "agents path agents\\a.md is not a relative path inside the plugin's directory",
			"root":    "skills path ./ names the plugin's own directory, not one of its skills",
			"twice":   "skills lists two paths named x",
			"none":    "its skills, commands and agents lists name nothing",
			"notlist": "skills is not a path or a list of paths",
			"hooked":  "the catalog declares its hooks; lola reads a plugin's components from its own files, never from the catalog",
		}))
		Expect(cat.Skipped).To(ContainElement(market.Skip{Name: "up", Reason: "skills path ./skills/../../x is not a relative path inside the plugin's directory",
			Kind: "path outside the plugin"}))
		Expect(market.CountKind(2, "path outside the plugin")).To(Equal("2 paths outside the plugin"))
		bound := market.Bind(cat, gitPin(commit))
		Expect(bound.Entries).To(HaveLen(2))
		doc, tools := bound.Entries[0], bound.Entries[1]
		Expect(doc.Components).To(Equal(&market.Components{Skills: []string{"skills/xlsx", "skills/docx"}}))
		Expect(doc.Source).To(Equal(manifest.Entry{Name: "document-skills", Source: "https://git.example.com/acme/plugins.git", Ref: "main",
			Commit: commit}), "the marketplace's own tree, at its commit")
		Expect(tools.Components).To(Equal(&market.Components{Commands: []string{"commands/fmt.md", "more"}, Agents: []string{"agents/reviewer.md"}}))
		Expect(market.Selected(cat, ".claude-plugin/marketplace.json")).To(Equal(map[string]string{"skills/xlsx": "skills", "skills/docx": "skills",
			"plugins/tools/commands/fmt.md": "commands", "plugins/tools/more": "commands", "plugins/tools/agents/reviewer.md": "agents"}))

		pin := gitPin(commit)
		pin.Absent = []string{"skills/docx", "plugins/tools/agents/reviewer.md"}
		Expect(reasons(market.Bind(cat, pin))).To(HaveKeyWithValue("document-skills", "skills/docx is not a directory in the marketplace"))
		Expect(reasons(market.Bind(cat, pin))).To(HaveKeyWithValue("tools", "agents/reviewer.md is not a file or directory in the marketplace"))
	})

	It("a catalog listing more than 1024 component paths for one entry skips it as oversized", func() {
		many := make([]string, 0, 1025)
		for i := range 1025 {
			many = append(many, fmt.Sprintf(`"./s/%d"`, i))
		}
		cat := parsed(`{"name": "big", "source": "./", "skills": [`+strings.Join(many[:1000], ", ")+`], "agents": [`+strings.Join(many[1000:], ", ")+`]}`,
			`{"name": "fits", "source": "./", "skills": [`+strings.Join(many[:1024], ", ")+`]}`)
		Expect(cat.Skipped).To(Equal([]market.Skip{{Name: "big", Reason: "its skills, commands and agents lists name more than 1024 paths",
			Kind: "oversized entry"}}))
		Expect(cat.Entries).To(HaveLen(1))
		Expect(cat.Entries[0].Components.Skills).To(HaveLen(1024))
	})

	It("a catalog component list on another repository skips the entry", func() {
		cat := parsed(`{"name": "gh", "source": {"source": "github", "repo": "acme/gh"}, "skills": ["./skills/a"]}`,
			`{"name": "u", "source": {"source": "url", "url": "https://git.example.com/u.git"}, "commands": ["./c.md"]}`,
			`{"name": "sub", "source": {"source": "git-subdir", "url": "acme/mono", "path": "p"}, "agents": ["./a.md"]}`,
			`{"name": "mcp", "source": "./m", "mcpServers": {"x": {"command": "x"}}}`)
		Expect(cat.Entries).To(BeEmpty())
		Expect(reasons(cat)).To(Equal(map[string]string{
			"gh":  "the catalog lists its skills, and it does not own that repository; lola reads such a plugin's components from its own files",
			"u":   "the catalog lists its commands, and it does not own that repository; lola reads such a plugin's components from its own files",
			"sub": "the catalog lists its agents, and it does not own that repository; lola reads such a plugin's components from its own files",
			"mcp": "the catalog declares its mcpServers; lola reads a plugin's components from its own files, never from the catalog",
		}))
		Expect(cat.Skipped).To(ContainElement(HaveField("Kind", "component list on another repository")))
		Expect(market.CountKind(2, "component list on another repository")).To(Equal("2 component lists on another repository"))
	})

	It("a malformed sha or sha256 skips the entry", func() {
		cat := parsed(`{"name": "short", "source": {"source": "github", "repo": "a/b", "sha": "abc123"}}`,
			`{"name": "upper", "source": {"source": "github", "repo": "a/b", "sha": "`+strings.ToUpper(commit)+`"}}`,
			`{"name": "disagree", "source": {"source": "github", "repo": "a/b", "ref": "`+commit+`", "sha": "`+other+`"}}`,
			`{"name": "agree", "source": {"source": "github", "repo": "a/b", "ref": "`+commit+`", "sha": "`+commit+`"}}`,
			`{"name": "sum", "source": {"source": "archive", "url": "https://e.example/a.zip", "sha256": "xyz"}}`,
			`{"name": "sha256", "source": {"source": "github", "repo": "a/b", "sha": "`+strings.Repeat("c", 64)+`"}}`)
		Expect(reasons(cat)).To(Equal(map[string]string{
			"short":    "sha abc123 is not a full commit SHA (40 or 64 lower-case hex digits)",
			"upper":    "sha " + strings.ToUpper(commit) + " is not a full commit SHA (40 or 64 lower-case hex digits)",
			"disagree": "ref and sha disagree: ref " + commit + ", sha " + other,
			"sum":      "sha256 xyz is not 64 hex digits",
		}))
		Expect(cat.Entries).To(HaveLen(2))
		Expect(cat.Entries[0].Source.Commit).To(Equal(commit))
		Expect(cat.Entries[1].Source.Commit).To(Equal(strings.Repeat("c", 64)), "lola takes a sha256 commit too")
	})

	It("a full-commit ref in capitals is that commit, lower-cased", func() {
		cat := parsed(`{"name": "caps", "source": {"source": "github", "repo": "a/b", "ref": "`+strings.ToUpper(commit)+`"}}`,
			`{"name": "agree", "source": {"source": "github", "repo": "a/b", "ref": "`+strings.ToUpper(commit)+`", "sha": "`+commit+`"}}`,
			`{"name": "disagree", "source": {"source": "github", "repo": "a/b", "ref": "`+strings.ToUpper(commit)+`", "sha": "`+other+`"}}`)
		Expect(reasons(cat)).To(Equal(map[string]string{"disagree": "ref and sha disagree: ref " + commit + ", sha " + other}))
		Expect(cat.Entries).To(HaveLen(2))
		for _, e := range cat.Entries {
			Expect(e.Source).To(And(HaveField("Ref", ""), HaveField("Commit", commit)), e.Name)
		}
	})

	It("a Claude archive entry's sha256 reaches source.Spec.SHA256", func() {
		sum := strings.Repeat("AB", 32)
		cat := market.Bind(parsed(`{"name": "bundle", "source": {"source": "archive", "url": "https://dl.example.com/b.tar.gz", "sha256": "`+sum+`"}}`,
			`{"name": "plain", "source": {"source": "archive", "url": "https://dl.example.com/p.zip"}}`,
			`{"name": "linter", "source": {"source": "url", "url": "https://git.example.com/linter.git", "ref": "v1", "sha": "`+commit+`"}}`,
			`{"name": "formatter", "source": "./tools/formatter"}`), gitPin(commit))
		specs := map[string]source.Spec{}
		for _, e := range cat.Entries {
			specs[e.Name] = e.Spec()
		}
		Expect(specs).To(Equal(map[string]source.Spec{
			"bundle": {Kind: source.KindArchive, Location: "https://dl.example.com/b.tar.gz", SHA256: strings.ToLower(sum), FromCatalog: true},
			"plain":  {Kind: source.KindArchive, Location: "https://dl.example.com/p.zip", FromCatalog: true},
			// The ref, never the commit: the commit is checked against the ref (Plan 6c §6.4).
			"linter": {Kind: source.KindGit, Location: "https://git.example.com/linter.git", Ref: "v1", FromCatalog: true},
			// A relative entry is the marketplace's own source, which you typed.
			"formatter": {Kind: source.KindGit, Location: "https://git.example.com/acme/plugins.git", Ref: "main", Path: "tools/formatter"},
		}))
	})

	It("two entries of one name skip both", func() {
		cat := parsed(`{"name": "twin", "source": "./a"}`, `{"name": "twin", "source": {"source": "npm", "package": "x"}}`,
			`{"name": "solo", "source": "./s"}`, `{"name": "Bad Name", "source": "./b"}`,
			`{"name": "amb", "source": "./a", "source": "./b"}`, `{"name": "amb", "source": "./c"}`)
		Expect(cat.Entries).To(HaveLen(1))
		Expect(cat.Skipped).To(ContainElement(market.Skip{Name: "amb", Reason: "the catalog lists amb twice; lola cannot tell which is meant",
			Kind: "name listed twice"}))
		Expect(cat.Skipped).To(HaveLen(5), "both amb entries and both twin entries are skipped, and Bad Name")
		Expect(cat.Entries[0].Name).To(Equal("solo"))
		Expect(cat.Skipped).To(ContainElement(market.Skip{Name: "twin", Reason: "the catalog lists twin twice; lola cannot tell which is meant",
			Kind: "name listed twice"}))
		Expect(cat.Skipped).To(ContainElement(HaveField("Reason", ContainSubstring(`entry name "Bad Name" is invalid`))))
	})
})

var _ = Describe("names and lookup", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("refuses verbs and invalid names, and slugs a display name", func() {
		Expect(market.ValidateName("acme")).To(Succeed())
		verbs := market.Verbs()
		verbs[0] = "changed"
		Expect(market.Verbs()[0]).To(Equal("add"), "a copy: no caller can change the reserved words")
		for _, v := range market.Verbs() {
			Expect(market.ValidateName(v)).To(MatchError("marketplace name "+v+" is reserved: lola market "+v+
				" answers with the lola command to run instead, so it cannot name a marketplace"), v)
		}
		Expect(market.ValidateName("Acme")).To(MatchError(ContainSubstring("is invalid")))
		Expect(market.Slug("Lola General Marketplace")).To(Equal("lola-general-marketplace"))
		Expect(market.Slug("  ..Acme   Tools!! ")).To(Equal("acme-tools"))
		Expect(market.Slug("日本")).To(Equal(""))
		Expect(len(market.Slug(strings.Repeat("ab ", 40)))).To(BeNumerically("<=", 64))
		taken := map[string]bool{"acme": true, "acme-2": true}
		Expect(market.FreeName("acme", 2, func(n string) bool { return taken[n] })).To(Equal("acme-3"))
		Expect(market.FreeName("list", 1, func(string) bool { return false })).To(Equal("list-2"), "never a verb")
		Expect([]string{market.CountKind(1, "npm source"), market.CountKind(2, "malformed entry"), market.CountKind(3, "missing directory"),
			market.CountKind(2, "source that is not https")}).To(Equal([]string{"1 npm source", "2 malformed entries", "3 missing directories",
			"2 sources that are not https"}))
		Expect(market.CatalogFiles("general")).To(Equal([]string{".claude-plugin/marketplace.json", "general.yml", "general.yaml",
			"marketplace.yml", "marketplace.yaml"}))
		Expect(market.CatalogFiles("")).To(Equal([]string{".claude-plugin/marketplace.json", "marketplace.yml", "marketplace.yaml"}))
		Expect(market.Root(".claude-plugin/marketplace.json")).To(Equal(""))
		Expect(market.Root("sub/.claude-plugin/marketplace.json")).To(Equal("sub"))
		Expect(market.Root("sub/general.yml")).To(Equal("sub"))
		Expect(market.IsCatalogURL("https://raw.example.com/m/general-market.yml")).To(BeTrue())
		Expect(market.IsCatalogURL("https://github.com/acme/plugins")).To(BeFalse())
	})

	It("reads @market/module and @market alone, refusing an address of an invalid name, and finds close names", func() {
		for arg, want := range map[string]market.Address{"@acme/formatter": {Market: "acme", Entry: "formatter"}, "@acme": {Market: "acme"}} {
			a, ok, err := market.ParseAddress(arg)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(a).To(Equal(want))
			Expect(a.String()).To(Equal(arg))
		}
		for _, arg := range []string{"formatter", "./@acme/x", "https://x/@acme"} {
			_, ok, err := market.ParseAddress(arg)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeFalse(), arg)
		}
		for arg, want := range map[string]string{
			"@Acme/x":     `@Acme/x is not a marketplace address, @<marketplace>/<module>: marketplace name "Acme" is invalid`,
			"@acme/":      `entry name "" is invalid`,
			"@/x":         `marketplace name "" is invalid`,
			"@acme/x/y":   `entry name "x/y" is invalid`,
			"@acme/a\x1b": `entry name "a?" is invalid`,
		} {
			_, ok, err := market.ParseAddress(arg)
			Expect(ok).To(BeTrue())
			Expect(err).To(MatchError(ContainSubstring(want)), arg)
		}
		_, _, err := market.ParseAddress("@acme/x/y")
		Expect(err).To(MatchError(HaveSuffix("(a directory of that name is ./@acme/x/y)")))

		cat := parsed(`{"name": "formatter", "source": "./f"}`, `{"name": "formats", "source": "./g"}`, `{"name": "fmt", "source": "./h"}`,
			`{"name": "code-formatter-pro", "source": "./i"}`, `{"name": "linter", "source": "./j"}`, `{"name": "npm-formatter", "source": {"source": "npm"}}`)
		Expect(market.CloseNames(cat, "formater")).To(Equal([]string{"formatter", "formats"}))
		Expect(market.CloseNames(cat, "FORMAT")).To(Equal([]string{"formats", "formatter", "code-formatter-pro"}), "a skipped entry is no candidate")
		Expect(market.CloseNames(cat, "zzz")).To(BeEmpty())
		var many []string
		for i := range 12 {
			many = append(many, fmt.Sprintf(`{"name": "tool-%02d", "source": "./t%d"}`, i, i))
		}
		Expect(market.CloseNames(parsed(many...), "tool")).To(HaveLen(10))
	})

	It("reads markets.yaml, refusing what lola would not record, naming the line", func() {
		reg, err := market.ParseRegistry("markets.yaml", []byte("# mine\nversion: 1\nmarkets:\n  - name: acme\n    source: https://github.com/acme/plugins\n"+
			"    ref: main\n    path: .claude-plugin/marketplace.json\n  - {name: local, source: /srv/market}\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(reg.Markets).To(Equal([]market.Registration{
			{Name: "acme", Source: "https://github.com/acme/plugins", Ref: "main", Path: ".claude-plugin/marketplace.json", Line: 4},
			{Name: "local", Source: "/srv/market", Line: 8}}))
		for in, want := range map[string]string{
			"version: 2\n": "markets.yaml was written by a newer lola (version 2)",
			"version: 1\nmarkets:\n  - {name: a, source: ./market}\n":                      "markets.yaml:3: marketplace a: source ./market is a relative path; record the folder's absolute path",
			"version: 1\nmarkets:\n  - {name: add, source: /m}\n":                          "markets.yaml:3: marketplace name add is reserved",
			"version: 1\nmarkets:\n  - {name: a, source: /m}\n  - {name: a, source: /n}\n": "markets.yaml:4: marketplace a is listed twice",
			"version: 1\nmarkets:\n  - {name: a, source: /m, extra: 1}\n":                  "field extra not found",
			"version: 1\nmarkets:\n  - {name: a, source: \"-x\"}\n":                        "starts with '-'",
			"version: 1\nmarkets:\n  - {name: a, source: /m, path: ../x}\n":                "must be a relative path inside the source",
		} {
			_, err := market.ParseRegistry("markets.yaml", []byte(in))
			Expect(err).To(MatchError(ContainSubstring(want)), in)
		}

		b, err := market.AddRegistration(nil, market.Registration{Name: "acme", Source: "https://github.com/acme/plugins", Path: ".claude-plugin/marketplace.json"})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("# lola marketplaces. Safe to edit by hand.\nversion: 1\nmarkets:\n  - name: acme\n" +
			"    source: https://github.com/acme/plugins\n    path: .claude-plugin/marketplace.json\n"))
		b, err = market.AddRegistration(b, market.Registration{Name: "local", Source: "/srv/market"})
		Expect(err).NotTo(HaveOccurred())
		b, found, err := market.RemoveRegistration(b, "acme")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(string(b)).To(Equal("# lola marketplaces. Safe to edit by hand.\nversion: 1\nmarkets:\n  - name: local\n    source: /srv/market\n"))
		_, found, err = market.RemoveRegistration(b, "nope")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("reads source.json, refusing a pin lola would not have written", func() {
		p := gitPin(strings.Repeat("a", 40))
		p.FetchedAt = time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
		b, err := json.Marshal(p)
		Expect(err).NotTo(HaveOccurred())
		good := b
		got, err := market.ParsePin(b)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(p))
		Expect(string(b)).NotTo(ContainSubstring("root"), "a tree at the source's top records no root, as a pin before root did not")
		wrapped := p
		wrapped.Root = "repo-abc"
		b, err = json.Marshal(wrapped)
		Expect(err).NotTo(HaveOccurred())
		Expect(market.ParsePin(b)).To(Equal(wrapped))
		for mutate, want := range map[string]func(*market.Pin){
			"commit is missing":                   func(p *market.Pin) { p.Commit = "" },
			"digest is missing":                   func(p *market.Pin) { p.Digest = "" },
			"written by a newer lola (version 2)": func(p *market.Pin) { p.Version = 2 },
			"../x is not a clean relative path":   func(p *market.Pin) { p.Absent = []string{"../x"} },
			"is not a .json, .yml or .yaml":       func(p *market.Pin) { p.File = "README.md" },
			"x/../y is not a clean relative path": func(p *market.Pin) { p.Root = "x/../y" },
			"a/ is not a clean relative path":     func(p *market.Pin) { p.Root = "a/" },
			`root is "."`:                         func(p *market.Pin) { p.Root = "." },
			"control or invisible characters":     func(p *market.Pin) { p.Root = "a\u0007" },
		} {
			bad := p
			want(&bad)
			b, err := json.Marshal(bad)
			Expect(err).NotTo(HaveOccurred())
			_, err = market.ParsePin(b)
			Expect(err).To(MatchError(ContainSubstring(mutate)), mutate)
		}
		_, err = market.ParsePin(append(append([]byte{}, good...), []byte(` {"version": 1}`)...))
		Expect(err).To(HaveOccurred(), "data after the object")
		nullAbsent := strings.Replace(string(good), `"absent":[]`, `"absent":null`, 1)
		Expect(nullAbsent).NotTo(Equal(string(good)))
		_, err = market.ParsePin([]byte(nullAbsent))
		Expect(err).To(MatchError(ContainSubstring("absent")))
		_, err = market.ParsePin([]byte(`{"version": 1, "extra": 1}`))
		Expect(err).To(MatchError(ContainSubstring("unknown field")))
	})
})
