package engine

import (
	"errors"
	"io"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
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
