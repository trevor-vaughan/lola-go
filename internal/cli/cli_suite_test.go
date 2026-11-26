package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
)

func TestCLI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "cli")
}

// result is one in-process lola invocation.
type result struct {
	out, err string
	code     int
}

// lola runs the real command tree in-process with buffered, non-terminal streams.
func lola(args ...string) result {
	root, err := cli.NewRootCmd(os.Getenv, cli.WithArgs(args))
	Expect(err).NotTo(HaveOccurred())
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(""))
	code := cli.Execute(root)
	return result{out: out.String(), err: errb.String(), code: code}
}

// projectContext is the context line of a project with no profile on, as scope commands print it
// first on stderr.
func projectContext(dir string) string { return "project " + dir + ": project.yml\n" }

// userContext is the context line of user scope with no profile on.
const userContext = "user scope: default\n"

// projectContextJSON is a project's context in a --json document: no profile on, writing nothing.
func projectContextJSON(dir string) map[string]any {
	return map[string]any{"scope": "project", "root": dir, "profile": nil, "writes_to": nil,
		"base": map[string]any{"name": "project.yml", "kind": "base", "path": filepath.Join(dir, ".lola", "project.yml")}}
}

// projectWriteContextJSON is the context member, comma included, that leads a project install's
// --json refusal: no profile on, writing project.yml.
func projectWriteContextJSON(dir string) string {
	base := `{"name":"project.yml","kind":"base","path":"` + filepath.Join(dir, ".lola", "project.yml") + `"}`
	return `"context":[{"scope":"project","root":"` + dir + `","base":` + base + `,"profile":null,"writes_to":` + base + `}],`
}

// withoutContext is a --json document as JSON without its context, once checkJSON passed the
// context: profile and writes_to there are null when unset, where the rest
// of a document holds no null.
func withoutContext(doc string) string {
	GinkgoHelper()
	var v map[string]any
	Expect(json.Unmarshal([]byte(doc), &v)).To(Succeed(), doc)
	if c, ok := v["context"]; ok {
		checkJSON(c, "context")
	}
	delete(v, "context")
	out, err := json.Marshal(v)
	Expect(err).NotTo(HaveOccurred())
	return string(out)
}

// lastHint is the text of the last hint: line of stderr, "" when there is none.
func lastHint(stderr string) string {
	i := strings.LastIndex(stderr, "hint: ")
	if i < 0 {
		return ""
	}
	line, _, _ := strings.Cut(stderr[i+len("hint: "):], "\n")
	return line
}

// expectRunnable fails unless hint, from its first "lola " on, names a real command whose flags
// parse and whose arguments its Args rule accepts.
func expectRunnable(hint string) {
	GinkgoHelper()
	_, cmdline, ok := strings.Cut(hint, "lola ")
	Expect(ok).To(BeTrue(), "no lola command in hint %q", hint)
	Expect(cli.ResolveHint(append([]string{"lola"}, shellWords(cmdline)...))).To(Succeed(), hint)
}
