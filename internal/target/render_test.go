package target_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Expand", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("expands lola's and Claude's plugin variables and leaves the project directory as authored", func() {
		ctx := target.PlanContext{PluginRoot: "/copies/m", PluginData: "/data/m"}
		Expect(target.Expand("${PLUGIN_ROOT} ${CLAUDE_PLUGIN_ROOT} ${PLUGIN_DATA} ${CLAUDE_PLUGIN_DATA} ${CLAUDE_PROJECT_DIR}", ctx)).
			To(Equal("/copies/m /copies/m /data/m /data/m ${CLAUDE_PROJECT_DIR}"))
	})
})
