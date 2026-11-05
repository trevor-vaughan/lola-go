package cli_test

import (
	"bytes"
	"os"
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
