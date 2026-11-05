package main_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

// lolaBin is the lola binary built for the suite, from the caller's environment, before any
// spec isolates it.
var lolaBin string

func TestLola(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "lola")
}

var _ = BeforeSuite(func() {
	var err error
	lolaBin, err = gexec.Build("github.com/trevor-vaughan/lola-go/cmd/lola")
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(gexec.CleanupBuildArtifacts)
