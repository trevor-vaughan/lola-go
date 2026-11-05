package engine_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/native/nativetest"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("module/native-executable finding", func() {
	const skillMD = "---\nname: demo\ndescription: Demo\n---\nbody\n"
	var (
		tmp, proj, kit string
		e              *engine.Engine
		sc             engine.Scope
		ctx            = context.Background()
		claude         = engine.Options{Targets: []string{"claude-code"}}
	)
	put := func(rel string, data []byte) {
		full := filepath.Join(kit, rel)
		Expect(os.MkdirAll(filepath.Dir(full), 0o755)).To(Succeed())
		Expect(os.WriteFile(full, data, 0o755)).To(Succeed())
	}
	install := func(opts engine.Options) (*engine.Result, error) {
		return e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: kit}}, opts)
	}
	native := func(fs []target.Finding) []target.Finding {
		var out []target.Finding
		for _, f := range fs {
			if f.Code == engine.CodeNativeExecutable {
				out = append(out, f)
			}
		}
		return out
	}

	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil }
		kit = filepath.Join(tmp, "src", "kit")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
		put("skills/demo/SKILL.md", []byte(skillMD))
	})

	It("warns once, naming the binary and what it is, and hints at lola info", func() {
		put("skills/demo/scripts/fmt", nativetest.ELF64())
		res, err := install(claude)
		Expect(err).NotTo(HaveOccurred())
		fs := native(res.Findings)
		Expect(fs).To(HaveLen(1))
		Expect(fs[0]).To(SatisfyAll(
			HaveField("Severity", target.SeverityWarn), HaveField("Module", "kit"),
			HaveField("Target", ""), HaveField("Kind", "module"),
			HaveField("Text", ContainSubstring("ships 1 native executable: skills/demo/scripts/fmt (ELF executable, x86-64")),
			HaveField("Hint", HavePrefix("lola info kit"))))
		Expect(filepath.Join(proj, ".claude/skills/demo/scripts/fmt")).To(BeAnExistingFile())
	})

	It("keeps the run's display flags in the install-time hint", func() {
		put("skills/demo/scripts/fmt", nativetest.ELF64())
		res, err := install(engine.Options{Targets: claude.Targets, Display: []string{"--plain"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(native(res.Findings)).To(ConsistOf(HaveField("Hint", HavePrefix("lola info kit --plain "))))
	})

	It("hints at lola info of the path lola check was given, with its display flags", func() {
		put("skills/demo/scripts/fmt", nativetest.ELF64())
		rep, err := e.Check(ctx, sc, proj, []string{kit}, nil, []string{"--plain"})
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules).To(HaveLen(1))
		Expect(native(rep.Modules[0].Findings)).To(ConsistOf(
			HaveField("Hint", HavePrefix("lola info "+errs.ShellJoin([]string{kit, "--plain"})+" lists each"))))
	})

	It("uses the right article for a malformed Mach-O and a malformed PE", func() {
		put("skills/demo/scripts/m", append([]byte{0xcf, 0xfa, 0xed, 0xfe}, make([]byte, 8)...))
		pe := append([]byte("MZ"), make([]byte, 62)...)
		pe[0x3c] = 0x40 // e_lfanew: the PE signature follows the 64-byte DOS header
		put("skills/demo/scripts/p", append(pe, 'P', 'E', 0, 0))
		res, err := install(claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(native(res.Findings)).To(ConsistOf(HaveField("Text", SatisfyAll(
			ContainSubstring("scripts/m (looks like a Mach-O file but is malformed)"),
			ContainSubstring("scripts/p (looks like a PE file but is malformed)")))))
	})

	It("does not warn for a binary outside every skill", func() {
		put("tests/fixtures/fmt", nativetest.ELF64())
		res, err := install(claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(native(res.Findings)).To(BeEmpty())
	})

	It("scans a root-level skill's whole directory except the entries lola reserves", func() {
		Expect(os.RemoveAll(filepath.Join(kit, "skills"))).To(Succeed())
		put("SKILL.md", []byte(skillMD))
		put("rules/bin/fmt", nativetest.ELF64())
		put("tests/fixtures/fmt", nativetest.ELF64())
		res, err := install(claude)
		Expect(err).NotTo(HaveOccurred())
		// tests/ is copied with the root skill (only lola's own entries are excluded), so it counts.
		Expect(filepath.Join(proj, ".claude/skills/kit/tests/fixtures/fmt")).To(BeAnExistingFile())
		Expect(filepath.Join(proj, ".claude/skills/kit/rules")).NotTo(BeAnExistingFile())
		fs := native(res.Findings)
		Expect(fs).To(HaveLen(1))
		Expect(fs[0].Text).To(ContainSubstring("ships 1 native executable: tests/fixtures/fmt ("))
	})

	It("shows three paths in sorted order and counts the rest", func() {
		for _, n := range []string{"e", "d", "c", "b", "a"} {
			put("skills/demo/bin/"+n, nativetest.ELF64())
		}
		res, err := install(claude)
		Expect(err).NotTo(HaveOccurred())
		fs := native(res.Findings)
		Expect(fs).To(HaveLen(1))
		Expect(fs[0].Text).To(SatisfyAll(
			ContainSubstring("ships 5 native executables: skills/demo/bin/a ("),
			ContainSubstring("), skills/demo/bin/b ("), ContainSubstring("), skills/demo/bin/c ("),
			HaveSuffix(" and 2 more"), Not(ContainSubstring("bin/d"))))
	})

	It("does not warn for a script", func() {
		put("skills/demo/scripts/run.sh", []byte("#!/bin/sh\necho hi\n"))
		put("skills/demo/scripts/class", nativetest.JavaClass())
		res, err := install(claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(native(res.Findings)).To(BeEmpty())
	})

	It("says so when a file has an ELF header the parser rejects", func() {
		put("skills/demo/scripts/x", append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 8)...))
		res, err := install(claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(native(res.Findings)).To(ConsistOf(HaveField("Text",
			ContainSubstring("skills/demo/scripts/x (looks like an ELF file but is malformed)"))))
	})

	It("is refused by --strict before anything is written, and installed without it", func() {
		put("skills/demo/scripts/fmt", nativetest.ELF64())
		_, err := install(engine.Options{Targets: claude.Targets, Strict: true})
		Expect(err).To(MatchError(ContainSubstring("--strict: 1 finding(s); nothing was changed")))
		Expect(err).To(MatchError(ContainSubstring("kit: ships 1 native executable")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("rerun without --strict")))
		Expect(filepath.Join(proj, ".claude")).NotTo(BeAnExistingFile())
	})

	It("is listed by Check under the module's findings", func() {
		put("skills/demo/scripts/fmt", nativetest.ELF64())
		rep, err := e.Check(ctx, sc, proj, []string{kit}, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules).To(HaveLen(1))
		Expect(native(rep.Modules[0].Findings)).To(HaveLen(1))
	})

	It("scans a command's sidecar directory", func() {
		put("commands/ship.md", []byte("Ship\n"))
		put("commands/ship/helper", nativetest.ELF64())
		res, err := install(claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(native(res.Findings)).To(ConsistOf(HaveField("Text", ContainSubstring("commands/ship/helper ("))))
	})
})
