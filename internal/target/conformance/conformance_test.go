// Package conformance_test holds the specs every registered target must pass. A new target
// gets them by being listed in internal/targets.
package conformance_test

import (
	"path"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

func doc(src string) module.Doc {
	d, err := module.ParseDoc([]byte(src))
	Expect(err).NotTo(HaveOccurred())
	return d
}

// kinds are the artifact kinds of the module IR.
var kinds = []string{"skill", "command", "agent", "mcp", "rule", "runtime-hook"}

// clean is a module every target should render without losing anything that matters.
func clean() *module.Module {
	return &module.Module{Name: "example",
		Skills:   []module.Skill{{Name: "review", Dir: "skills/review", Doc: doc("---\nname: review\ndescription: Review code\n---\nReview it.\n")}},
		Commands: []module.Command{{Name: "ship", Doc: doc("---\ndescription: Ship it\n---\nShip $ARGUMENTS\n")}},
		Agents:   []module.Agent{{Name: "critic", Doc: doc("---\ndescription: Critic\n---\nCriticize.\n")}},
		MCPServers: []module.MCPServer{
			{Name: "gh", Transport: module.TransportStdio, Command: "${PLUGIN_ROOT}/bin/gh", Args: []string{"--stdio"}},
			{Name: "web", Transport: module.TransportHTTP, URL: "https://example.com/mcp"},
		},
		Rules: []module.Rule{{Name: "style", Body: "Prefer small diffs.\n"}},
	}
}

// wrong is a module with every problem some target cannot express.
func wrong() *module.Module {
	return &module.Module{Name: "example",
		Skills: []module.Skill{
			{Name: "bare", Dir: "skills/bare", Doc: doc("---\nuser-invocable: no\n---\nNo description.\n")},
			{Name: "my_skill", Dir: "skills/my_skill", Doc: doc("---\nname: other\ndescription: d\n---\nx\n")},
		},
		Commands: []module.Command{{Name: "status", Doc: doc("---\nallowed-tools: Bash\nmode: 0755\n---\nRun !`git status` with $1\n")}},
		Agents: []module.Agent{
			{Name: "rev", Doc: doc("---\ntools: Read, Grep\nmodel: opus\ncolor: red\n---\nNo description.\n")},
			{Name: "v1.2", Doc: doc("---\ndescription: d\ntools: {write: false}\n---\nx\n")},
		},
		MCPServers: []module.MCPServer{{Name: "env", Transport: module.TransportStdio, Command: "srv", Cwd: "/tmp",
			Env: map[string]string{"LEVEL": "${LOG_LEVEL:-info}"}}},
		Rules:        []module.Rule{{Name: "style", Body: "Keep ${PLUGIN_ROOT} literal.\n"}},
		RuntimeHooks: []module.RuntimeHookSet{{Source: "hooks/hooks.json", Hooks: []byte(`{"PreToolUse":[{"hooks":[{"command":"x","type":"command"}]}]}`)}},
	}
}

// alone splits m into modules of one artifact each, so a module's intents and findings are
// that artifact's.
func alone(m *module.Module) []*module.Module {
	var out []*module.Module
	for _, s := range m.Skills {
		out = append(out, &module.Module{Name: m.Name, Skills: []module.Skill{s}})
	}
	for _, c := range m.Commands {
		out = append(out, &module.Module{Name: m.Name, Commands: []module.Command{c}})
	}
	for _, a := range m.Agents {
		out = append(out, &module.Module{Name: m.Name, Agents: []module.Agent{a}})
	}
	for _, s := range m.MCPServers {
		out = append(out, &module.Module{Name: m.Name, MCPServers: []module.MCPServer{s}})
	}
	for _, r := range m.Rules {
		out = append(out, &module.Module{Name: m.Name, Rules: []module.Rule{r}})
	}
	for _, h := range m.RuntimeHooks {
		out = append(out, &module.Module{Name: m.Name, RuntimeHooks: []module.RuntimeHookSet{h}})
	}
	return out
}

// restrict sets the per-entry targets of every artifact in m.
func restrict(m *module.Module, ids []string) {
	for i := range m.Skills {
		m.Skills[i].Targets = ids
	}
	for i := range m.Commands {
		m.Commands[i].Targets = ids
	}
	for i := range m.Agents {
		m.Agents[i].Targets = ids
	}
	for i := range m.MCPServers {
		m.MCPServers[i].Targets = ids
	}
	for i := range m.Rules {
		m.Rules[i].Targets = ids
	}
	for i := range m.RuntimeHooks {
		m.RuntimeHooks[i].Targets = ids
	}
}

var _ = Describe("every registered target", func() {
	for _, t := range targets.All() {
		for _, scope := range []target.Scope{target.Project, target.User} {
			Context(t.ID()+" at "+scope.String()+" scope", func() {
				var ctx target.PlanContext
				BeforeEach(func() {
					ctx = target.PlanContext{Scope: scope, Root: testenv.Isolate(), ContentDir: "/cache/example/content",
						PluginRoot: "/copies/example", PluginData: "/data/example"}
				})
				run := func(m *module.Module, c target.PlanContext) ([]plan.Intent, []target.Finding) {
					ins, fs, err := t.Plan(m, c)
					Expect(err).NotTo(HaveOccurred(), "content problems are findings, never Plan errors")
					return ins, fs
				}

				It("plans the same intents and findings every time", func() {
					for _, m := range []func() *module.Module{clean, wrong} {
						ins1, fs1 := run(m(), ctx)
						ins2, fs2 := run(m(), ctx)
						Expect(ins2).To(Equal(ins1))
						Expect(fs2).To(Equal(fs1))
					}
				})

				It("writes files only under its base directories and keeps every path relative and clean", func() {
					bases := t.BaseDirs(scope)
					for _, m := range []func() *module.Module{clean, wrong} {
						force := ctx
						force.Force = true
						ins, _ := run(m(), force)
						for _, in := range ins {
							Expect(path.IsAbs(in.Path)).To(BeFalse(), in.Path)
							Expect(path.Clean(in.Path)).To(Equal(in.Path))
							Expect(in.Path).NotTo(HavePrefix(".."))
							if in.Op == plan.OpWriteFile || in.Op == plan.OpCopyTree {
								Expect(slices.ContainsFunc(bases, func(b string) bool { return strings.HasPrefix(in.Path, b+"/") })).
									To(BeTrue(), "%s is outside %v", in.Path, bases)
							}
						}
					}
				})

				It("renders the clean module without skipping anything", func() {
					_, fs := run(clean(), ctx)
					for _, f := range fs {
						Expect(f.Severity).NotTo(Equal(target.SeveritySkip), f.Describe())
					}
				})

				It("reports well-formed findings about itself", func() {
					_, fs := run(wrong(), ctx)
					Expect(fs).NotTo(BeEmpty())
					for _, f := range fs {
						Expect(f.Target).To(Equal(t.ID()))
						Expect([]target.Severity{target.SeverityInfo, target.SeverityWarn, target.SeveritySkip}).To(ContainElement(f.Severity))
						Expect(f.Code).NotTo(BeEmpty())
						Expect(f.Text).NotTo(BeEmpty())
						Expect(kinds).To(ContainElement(f.Kind))
						Expect(f.Forceable && f.Severity != target.SeveritySkip).To(BeFalse(), "only skips are forceable: %s", f.Describe())
					}
				})

				It("plans nothing for a skipped artifact, and renders a forceable one only with Force", func() {
					for _, m := range alone(wrong()) {
						ins, fs := run(m, ctx)
						skips := slices.DeleteFunc(slices.Clone(fs), func(f target.Finding) bool { return f.Severity != target.SeveritySkip })
						if len(skips) == 0 {
							continue
						}
						Expect(ins).To(BeEmpty(), skips[0].Describe())
						force := ctx
						force.Force = true
						ins, fs = run(m, force)
						if slices.ContainsFunc(skips, func(f target.Finding) bool { return !f.Forceable }) {
							Expect(ins).To(BeEmpty(), skips[0].Describe())
							continue
						}
						Expect(ins).NotTo(BeEmpty(), skips[0].Describe())
						Expect(fs).To(ContainElement(HaveField("Forced", true)))
					}
				})

				It("reports runtime hooks as kind-unsupported and never renders them, even with Force", func() {
					m := &module.Module{Name: "example", RuntimeHooks: wrong().RuntimeHooks}
					force := ctx
					force.Force = true
					for _, c := range []target.PlanContext{ctx, force} {
						ins, fs := run(m, c)
						Expect(ins).To(BeEmpty())
						Expect(fs).To(ConsistOf(MatchFields(IgnoreExtras, Fields{"Severity": Equal(target.SeveritySkip),
							"Kind": Equal("runtime-hook"), "Artifact": Equal("hooks/hooks.json"), "Code": Equal("kind-unsupported"), "Forceable": BeFalse()})))
					}
				})

				It("renders an artifact restricted to targets only on those targets", func() {
					for _, m := range alone(clean()) {
						elsewhere, here := m, m
						restrict(elsewhere, []string{"some-other-target"})
						ins, fs := run(elsewhere, ctx)
						Expect(ins).To(BeEmpty())
						Expect(fs).To(BeEmpty())
						restrict(here, []string{"some-other-target", t.ID()})
						ins, _ = run(here, ctx)
						Expect(ins).NotTo(BeEmpty())
					}
				})

				It("plans only the kinds it declares", func() {
					declared := t.Capabilities().Kinds
					for _, m := range []func() *module.Module{clean, wrong} {
						ins, _ := run(m(), ctx)
						for _, in := range ins {
							Expect(declared).To(HaveKey(in.Kind), in.Path)
						}
					}
					for k := range declared {
						Expect(kinds).To(ContainElement(k))
					}
				})
			})
		}
	}
})
