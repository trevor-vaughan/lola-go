package fsx_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/fsx"
)

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
			got, created, err := fsx.JSONAddMember([]byte(src), ptr, member)
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
			got, err := fsx.JSONRemoveMember([]byte(src), "/context/includeDirectories", member, dropEmpty)
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

	It("JSONHasMember finds only string members of an array", func() {
		src := []byte(`{"i": ["a", 1, {"b": 2}]}`)
		Expect(fsx.JSONHasMember(src, "/i", "a")).To(BeTrue())
		Expect(fsx.JSONHasMember(src, "/i", "b")).To(BeFalse())
		Expect(fsx.JSONHasMember(src, "/missing", "a")).To(BeFalse())
	})

	It("refuses array edits where the value is not an array", func() {
		src := []byte(`{"i": "a"}`)
		_, err := fsx.JSONHasMember(src, "/i", "a")
		Expect(err).To(MatchError("/i is not an array"))
		_, _, err = fsx.JSONAddMember(src, "/i", "a")
		Expect(err).To(MatchError("/i is not an array"))
		_, err = fsx.JSONRemoveMember(src, "/i", "a", true)
		Expect(err).To(MatchError("/i is not an array"))
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
		got, _, err := fsx.JSONAddMember([]byte(arr), "/i", "m")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("{\n  \"i\": [\n    \"a\",\n    \"m\"\n    // \"b\"\n  ]\n}\n"))
		back, err := fsx.JSONRemoveMember(got, "/i", "m", false)
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
