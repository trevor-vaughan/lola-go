package targets_test

import (
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
)

func doc(src string) module.Doc {
	d, err := module.ParseDoc([]byte(src))
	Expect(err).NotTo(HaveOccurred())
	return d
}

// byPath indexes intents by Path (and Pointer for JSON members).
func byPath(ins []plan.Intent) map[string]plan.Intent {
	out := map[string]plan.Intent{}
	for _, in := range ins {
		out[in.Path+in.Pointer] = in
	}
	return out
}

// codes lists each finding as "<severity> <code> <artifact>".
func codes(fs []target.Finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, string(f.Severity)+" "+f.Code+" "+f.Artifact)
	}
	return out
}

// builtin is the registered target with id.
func builtin(id string) target.Target {
	t, ok := targets.Builtin().Lookup(id)
	Expect(ok).To(BeTrue(), id)
	return t
}
