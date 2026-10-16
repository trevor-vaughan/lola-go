package fsx_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFsx(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "fsx")
}
