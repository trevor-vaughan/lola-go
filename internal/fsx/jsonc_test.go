package fsx_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/fsx"
)

// str is s as a JSON string member.
func str(s string) []byte {
	b, err := json.Marshal(s)
	Expect(err).NotTo(HaveOccurred())
	return b
}

var _ = Describe("JSONC edits", func() {
	DescribeTable("JSONSet preserves every byte outside the edited member",
		func(src, ptr, val, want string) {
			got, err := fsx.JSONSet([]byte(src), ptr, []byte(val))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(got)).To(Equal(want))
		},
		Entry("appends to a commented, hand-formatted object",
			"{\n  // c\n  \"theme\":   \"dark\",   // keep\n  \"mcpServers\": {\n    \"mine\": {\"command\": \"x\"} // users\n  }\n}\n",
			"/mcpServers/gh", `{"command":"npx","args":["-y"]}`,
			"{\n  // c\n  \"theme\":   \"dark\",   // keep\n  \"mcpServers\": {\n    \"mine\": {\"command\": \"x\"}, // users\n    \"gh\": {\n      \"command\": \"npx\",\n      \"args\": [\n        \"-y\"\n      ]\n    }\n  }\n}\n"),
		Entry("keeps a trailing-comma style", "{\n  \"a\": 1,\n}\n", "/b", `2`, "{\n  \"a\": 1,\n  \"b\": 2,\n}\n"),
		Entry("creates missing parents in an empty file", "", "/mcpServers/gh", `{"command":"x"}`,
			"{\n  \"mcpServers\": {\n    \"gh\": {\n      \"command\": \"x\"\n    }\n  }\n}\n"),
		Entry("stays inline in a one-line object", `{"a":1}`, "/b", `{"x":1}`, `{"a":1, "b":{"x":1}}`),
		Entry("replaces a value and keeps its line comment", "{\n  \"a\": 1, // one\n  \"b\": 2\n}", "/a", `5`, "{\n  \"a\": 5, // one\n  \"b\": 2\n}"),
		Entry("unescapes RFC 6901 tokens", "{}", "/a~1b", `1`, "{\n  \"a/b\": 1\n}"),
	)

	DescribeTable("JSONDelete removes only the member and its own comments",
		func(src, ptr, want string) {
			got, err := fsx.JSONDelete([]byte(src), ptr)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(got)).To(Equal(want))
		},
		Entry("last member: the previous line's comment survives",
			"{\n  \"a\": 1, // c\n  \"b\": 2 // d\n}", "/b", "{\n  \"a\": 1 // c\n}"),
		Entry("middle member: its doc comment and trailing comment go",
			"{\n  \"a\": 1,\n  // about b\n  \"b\": 2, // b trailing\n  \"c\": 3\n}", "/b", "{\n  \"a\": 1,\n  \"c\": 3\n}"),
		Entry("prunes a parent object that became empty",
			"{\n  \"x\": 1,\n  \"mcpServers\": {\n    \"gh\": {}\n  }\n}\n", "/mcpServers/gh", "{\n  \"x\": 1\n}\n"),
		Entry("is a no-op when the member is missing", "{\"a\":1}", "/b/c", "{\"a\":1}"),
	)

	DescribeTable("JSONAddMember appends one string and preserves the rest",
		func(src, ptr, member, want string, wantCreated bool) {
			got, created, err := fsx.JSONAddMember([]byte(src), ptr, str(member))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(got)).To(Equal(want))
			Expect(created).To(Equal(wantCreated))
		},
		Entry("keeps a comment on the line it ended",
			"{\n  \"context\": {\n    \"includeDirectories\": [\n      \"docs\" // mine\n    ]\n  }\n}\n",
			"/context/includeDirectories", ".gemini/lola/m",
			"{\n  \"context\": {\n    \"includeDirectories\": [\n      \"docs\", // mine\n      \".gemini/lola/m\"\n    ]\n  }\n}\n", false),
		Entry("keeps a trailing-comma style", "{\n  \"instructions\": [\n    \"a.md\",\n  ]\n}\n", "/instructions", "b",
			"{\n  \"instructions\": [\n    \"a.md\",\n    \"b\",\n  ]\n}\n", false),
		Entry("stays inline in a one-line array", `{"instructions": ["a.md"]}`, "/instructions", "b",
			`{"instructions": ["a.md", "b"]}`, false),
		Entry("fills an empty array", "{\n  \"instructions\": []\n}\n", "/instructions", "b",
			"{\n  \"instructions\": [\n    \"b\"\n  ]\n}\n", false),
		Entry("creates the array and its parents in an empty file", "", "/context/includeDirectories", ".gemini/lola/m",
			"{\n  \"context\": {\n    \"includeDirectories\": [\n      \".gemini/lola/m\"\n    ]\n  }\n}\n", true),
		Entry("leaves an array that already has the member untouched", "{\"instructions\": [ \"b\" ] }", "/instructions", "b",
			"{\"instructions\": [ \"b\" ] }", false),
	)

	DescribeTable("JSONRemoveMember removes one string and its own comments",
		func(src, member string, dropEmpty bool, want string) {
			got, err := fsx.JSONRemoveMember([]byte(src), "/context/includeDirectories", str(member), dropEmpty)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(got)).To(Equal(want))
		},
		Entry("last element: the previous line's comment survives",
			"{\n  \"context\": {\n    \"includeDirectories\": [\n      \"a\", // a\n      \"b\" // b\n    ]\n  }\n}\n", "b", false,
			"{\n  \"context\": {\n    \"includeDirectories\": [\n      \"a\" // a\n    ]\n  }\n}\n"),
		Entry("first element of an inline array", `{"context": {"includeDirectories": ["a", "b"]}}`, "a", false,
			`{"context": {"includeDirectories": ["b"]}}`),
		Entry("drops an emptied array and the parents it leaves empty",
			"{\n  \"theme\": \"x\",\n  \"context\": {\n    \"includeDirectories\": [\n      \"m\"\n    ]\n  }\n}\n", "m", true,
			"{\n  \"theme\": \"x\"\n}\n"),
		Entry("keeps an emptied array when told to",
			"{\n  \"context\": {\n    \"includeDirectories\": [\n      \"m\"\n    ]\n  }\n}\n", "m", false,
			"{\n  \"context\": {\n    \"includeDirectories\": [\n    ]\n  }\n}\n"),
		Entry("keeps an array that still has other members", `{"context": {"includeDirectories": ["a", "m"]}}`, "m", true,
			`{"context": {"includeDirectories": ["a"]}}`),
		Entry("is a no-op when the member is missing", `{"context": {"includeDirectories": ["a"]}}`, "m", true,
			`{"context": {"includeDirectories": ["a"]}}`),
		Entry("is a no-op when the array is missing", `{"a": 1}`, "m", true, `{"a": 1}`),
	)

	It("JSONHasMember compares members as canonical JSON", func() {
		src := []byte(`{"i": ["a", 1, {"b": 2, "a": [1, "x"]}, /* c */ "\u0062x"]}`)
		Expect(fsx.JSONHasMember(src, "/i", str("a"))).To(BeTrue())
		Expect(fsx.JSONHasMember(src, "/i", str("b"))).To(BeFalse())
		Expect(fsx.JSONHasMember(src, "/i", str("bx"))).To(BeTrue())
		Expect(fsx.JSONHasMember(src, "/i", []byte(`1`))).To(BeTrue())
		Expect(fsx.JSONHasMember(src, "/i", []byte(`{ "a": [1,"x"], "b": 2 }`))).To(BeTrue())
		Expect(fsx.JSONHasMember(src, "/i", []byte(`{"a":[1,"x"],"b":3}`))).To(BeFalse())
		Expect(fsx.JSONHasMember(src, "/missing", str("a"))).To(BeFalse())
	})

	It("refuses array edits where the value is not an array, or the member is not JSON", func() {
		src := []byte(`{"i": "a"}`)
		_, err := fsx.JSONHasMember(src, "/i", str("a"))
		Expect(err).To(MatchError(`"/i" is not an array`))
		_, _, err = fsx.JSONAddMember(src, "/i", str("a"))
		Expect(err).To(MatchError(`"/i" is not an array`))
		_, err = fsx.JSONRemoveMember(src, "/i", str("a"), true)
		Expect(err).To(MatchError(`"/i" is not an array`))
		_, err = fsx.JSONHasMember([]byte(`{"h": {"e\u001b": "x"}}`), "/h/e\x1b", str("a"))
		Expect(err).To(MatchError(`"/h/e\x1b" is not an array`))
		_, _, err = fsx.JSONAddMember([]byte(`{}`), "/i", []byte(`{x`))
		Expect(err).To(HaveOccurred())
	})

	Describe("object members", func() {
		group := []byte(`{"matcher":"Bash","hooks":[{"type":"command","command":"x"}]}`)

		It("adds an object indented like its siblings, once, and removes it whatever its key order", func() {
			user := "{\n  \"hooks\": {\n    \"Stop\": [\n      {\"hooks\": []} // mine\n    ]\n  }\n}\n"
			got, created, err := fsx.JSONAddMember([]byte(user), "/hooks/Stop", group)
			Expect(err).NotTo(HaveOccurred())
			Expect(created).To(BeFalse())
			Expect(string(got)).To(Equal("{\n  \"hooks\": {\n    \"Stop\": [\n      {\"hooks\": []}, // mine\n" +
				"      {\n        \"hooks\": [\n          {\n            \"command\": \"x\",\n            \"type\": \"command\"\n          }\n        ],\n" +
				"        \"matcher\": \"Bash\"\n      }\n    ]\n  }\n}\n"))
			again, _, err := fsx.JSONAddMember(got, "/hooks/Stop", []byte(`{"hooks":[{"command":"x","type":"command"}],"matcher":"Bash"}`))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(again)).To(Equal(string(got)))
			back, err := fsx.JSONRemoveMember(got, "/hooks/Stop", []byte(` { "matcher" : "Bash", "hooks":[{"command":"x","type":"command"}]}`), true)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(back)).To(Equal(user))
		})

		It("creates the event array and /hooks, and drops both once the array it created empties", func() {
			src := "{\n  \"model\": \"x\"\n}\n"
			got, created, err := fsx.JSONAddMember([]byte(src), "/hooks/Stop", group)
			Expect(err).NotTo(HaveOccurred())
			Expect(created).To(BeTrue())
			Expect(fsx.JSONHasMember(got, "/hooks/Stop", group)).To(BeTrue())
			back, err := fsx.JSONRemoveMember(got, "/hooks/Stop", group, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(back)).To(Equal(src))
		})

		DescribeTable("writes & < > as themselves, so a shell operator reads as one, and still finds and removes the member",
			func(src string) {
				shell := []byte(`{"hooks":[{"type":"command","command":"a && b > c < d"}]}`)
				got, _, err := fsx.JSONAddMember([]byte(src), "/hooks/Stop", shell)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(got)).To(ContainSubstring(`"a && b > c < d"`))
				Expect(string(got)).NotTo(ContainSubstring(`\u00`))
				Expect(fsx.JSONHasMember(got, "/hooks/Stop", shell)).To(BeTrue())
				again, _, err := fsx.JSONAddMember(got, "/hooks/Stop", shell)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(again)).To(Equal(string(got)), "the unescaped member is the same member")
				back, err := fsx.JSONRemoveMember(got, "/hooks/Stop", shell, true)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(back)).To(Equal(src))
			},
			Entry("into an existing array", "{\n  \"hooks\": {\n    \"Stop\": [\n      {\"hooks\": []}\n    ]\n  }\n}\n"),
			Entry("into an array it creates", "{\n  \"model\": \"x\"\n}\n"),
		)
	})

	It("CanonicalJSON escapes & < > as before, for hashes and records; ReadableJSON writes them as themselves", func() {
		Expect(fsx.CanonicalJSON([]byte(`{"b":"a && b > c","a":"<x>"}`))).To(Equal([]byte(`{"a":"\u003cx\u003e","b":"a \u0026\u0026 b \u003e c"}`)))
		Expect(fsx.ReadableJSON([]byte(`{"b":"a && b > c", "a":"<x>"}`))).To(Equal([]byte(`{"a":"<x>","b":"a && b > c"}`)))
		_, err := fsx.ReadableJSON([]byte(`{x`))
		Expect(err).To(HaveOccurred())
	})

	It("keeps comments after the last member or element, and removing lola's again restores the file", func() {
		for _, c := range []struct{ src, want string }{
			{"{\n  // only a comment\n}\n", "{\n  \"mcp\": {\n    \"x\": 1\n  }\n  // only a comment\n}\n"},
			{"{\n  \"a\": 1\n  // \"b\": 2\n}\n", "{\n  \"a\": 1,\n  \"mcp\": {\n    \"x\": 1\n  }\n  // \"b\": 2\n}\n"},
			{"{  // on the brace line\n}\n", "{  // on the brace line\n  \"mcp\": {\n    \"x\": 1\n  }\n}\n"},
		} {
			got, err := fsx.JSONSet([]byte(c.src), "/mcp/x", []byte("1"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(got)).To(Equal(c.want))
			back, err := fsx.JSONDelete(got, "/mcp/x")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(back)).To(Equal(c.src))
		}
		arr := "{\n  \"i\": [\n    \"a\"\n    // \"b\"\n  ]\n}\n"
		got, _, err := fsx.JSONAddMember([]byte(arr), "/i", str("m"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("{\n  \"i\": [\n    \"a\",\n    \"m\"\n    // \"b\"\n  ]\n}\n"))
		back, err := fsx.JSONRemoveMember(got, "/i", str("m"), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(back)).To(Equal(arr))
	})

	It("JSONGet returns canonical JSON with comments and trailing commas removed", func() {
		v, found, err := fsx.JSONGet([]byte("{\"m\": {\"b\": 1, /*x*/ \"a\": [1,2,],},}"), "/m")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(string(v)).To(Equal(`{"a":[1,2],"b":1}`))
		_, found, err = fsx.JSONGet([]byte(`{"m":{}}`), "/m/x")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("refuses to edit a corrupt file and reports the position", func() {
		_, err := fsx.JSONSet([]byte("{\n \"a\": 1,,\n}"), "/b", []byte("1"))
		Expect(err).To(MatchError(ContainSubstring("line 2")))
		_, err = fsx.JSONDelete([]byte("[1]"), "/a")
		Expect(err).To(MatchError(ContainSubstring("not an object")))
	})

	It("JSONIsEmptyObject ignores whitespace and comments", func() {
		Expect(fsx.JSONIsEmptyObject([]byte("{\n}\n"))).To(BeTrue())
		Expect(fsx.JSONIsEmptyObject([]byte("{\"a\":1}"))).To(BeFalse())
	})

	It("rejects malformed pointers", func() {
		for _, p := range []string{"", "a", "/a//b"} {
			_, err := fsx.JSONPointerTokens(p)
			Expect(err).To(HaveOccurred(), p)
		}
	})
})
