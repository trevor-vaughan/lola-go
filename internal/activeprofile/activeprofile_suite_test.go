package activeprofile_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestActiveProfile(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "activeprofile")
}
