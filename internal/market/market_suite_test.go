package market_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMarket(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "market")
}
