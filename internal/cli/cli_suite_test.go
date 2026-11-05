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
	root := cli.NewRootCmd(os.Getenv)
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
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
// context: profile and writes_to there are null when unset (spec 2026-10-29 §3.8), where the rest
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
