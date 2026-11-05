package engine_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
)

// confirmAll answers yes to the files an install that changes picks deletes (Pick-P2-b), as -y does.
func confirmAll([]engine.Removal) (bool, error) { return true, nil }

func TestEngine(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "engine")
}
