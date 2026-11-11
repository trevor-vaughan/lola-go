package mech_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMech(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "mech")
}
