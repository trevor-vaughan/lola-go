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
