package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/target/spec"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/targetspec"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("reading an assistant's version through its probes", func() {
	var (
		e    *Engine
		root string
	)
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = New(p, targets.Builtin())
		root = filepath.Join(p.Home, ".quill")
		Expect(os.MkdirAll(root, 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(tmp, "bin"), 0o755)).To(Succeed())
		e.Paths.Search = []string{filepath.Join(tmp, "bin")}
	})
	// quill is a target whose probe list is probes, one YAML flow mapping per line.
	quill := func(probes ...string) target.Target {
		GinkgoHelper()
		y := "id: quill\nprobe:\n"
		for _, p := range probes {
			y += "  - " + p + "\n"
		}
		if !strings.Contains(y, "exec:") {
			y += "readers: the quill editor\n"
		}
		y += "roots: {project: \"{project}\", user: \"{home}\"}\ndetect: {project: {any: [.quill/]}, user: {any: [.quill/]}}\n" +
			"eras:\n  - since: \"1.0\"\n    layout:\n      agent: {mech: markdown-file, path: .quill/agents, ext: .md}\n"
		s, err := targetspec.Parse("quill.yaml", []byte(y))
		Expect(err).NotTo(HaveOccurred())
		return spec.New(s)
	}
	put := func(name, content string) string {
		path := filepath.Join(root, name)
		Expect(os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
		return path
	}
	binary := func(name, script string) {
		Expect(os.WriteFile(filepath.Join(e.Paths.Search[0], name), []byte("#!/bin/sh\n"+script), 0o755)).To(Succeed())
	}
	read := func(t target.Target) (string, *target.Finding) {
		return e.newProbeRun(context.Background()).read(t)
	}
	info := func(code, text string) *target.Finding {
		return &target.Finding{Severity: target.SeverityInfo, Target: "quill", Kind: "assistant", Code: code, Text: text}
	}
	const (
		pointer = `file: {path: "{home}/.quill/state.json", pointer: /app/version}`
		match   = `file: {path: "{home}/.quill/VERSION", match: 'quill (\d+\.\d+\.\d+)'}`
		run     = `exec: {binaries: [quill], args: [--version], match: '(\d+\.\d+\.\d+)'}`
	)

	It("reads a version from a JSON file by its pointer", func() {
		put("state.json", `{"app": {"version": "1.4.0"}}`)
		Expect(read(quill(pointer))).To(Equal("1.4.0"))
	})

	It("reads a version from a file by its pattern", func() {
		put("VERSION", "quill 2.0.1\n")
		Expect(read(quill(match))).To(Equal("2.0.1"))
	})

	It("follows a pointer token holding an escaped slash", func() {
		put("state.json", `{"a/b": {"v~x": "3.0.0"}}`)
		Expect(read(quill(`file: {path: "{home}/.quill/state.json", pointer: /a~1b/v~0x}`))).To(Equal("3.0.0"))
	})

	It("runs the binary when the file is missing", func() {
		binary("quill", "echo 'quill 5.6.7'\n")
		Expect(read(quill(pointer, run))).To(Equal("5.6.7"))
	})

	It("reads the file when the binary is not on PATH", func() {
		put("state.json", `{"app": {"version": "1.4.0"}}`)
		Expect(read(quill(run, pointer))).To(Equal("1.4.0"))
	})

	It("says the first binary is not on PATH when nothing is found", func() {
		v, f := read(quill(pointer, run))
		Expect(v).To(BeEmpty())
		Expect(f).To(Equal(info("probe/not-found", "quill is not on PATH")))
	})

	It("says the first file does not exist when there is no binary to run", func() {
		v, f := read(quill(pointer, match))
		Expect(v).To(BeEmpty())
		Expect(f).To(Equal(info("probe/not-found", filepath.Join(root, "state.json")+" does not exist")))
	})

	It("reports a binary that cannot start without the path of the failed exec", func() {
		bin := filepath.Join(e.Paths.Search[0], "quill")
		Expect(os.WriteFile(bin, []byte("#!/nonexistent/interpreter\n"), 0o755)).To(Succeed())
		_, f := read(quill(run))
		Expect(f).To(Equal(info("probe/unknown-version", bin+" --version failed: no such file or directory")))
	})

	It("reports a directory as no regular file, ahead of a later method found missing", func() {
		Expect(os.Mkdir(filepath.Join(root, "state.json"), 0o755)).To(Succeed())
		v, f := read(quill(pointer, run))
		Expect(v).To(BeEmpty())
		Expect(f).To(Equal(info("probe/unknown-version", filepath.Join(root, "state.json")+" is not a regular file")))
	})

	It("reports a FIFO as no regular file without opening it", func() {
		path := filepath.Join(root, "state.json")
		if err := mkfifoForTest(path); err != nil {
			Skip(err.Error())
		}
		done := make(chan *target.Finding)
		go func() {
			_, f := read(quill(pointer))
			done <- f
		}()
		Eventually(done).WithTimeout(5 * time.Second).Should(Receive(Equal(info("probe/unknown-version", path+" is not a regular file"))))
	})

	It("reports a link to a device as no regular file", func() {
		if runtime.GOOS == "windows" {
			Skip("no /dev/zero")
		}
		path := filepath.Join(root, "state.json")
		Expect(os.Symlink("/dev/zero", path)).To(Succeed())
		_, f := read(quill(pointer))
		Expect(f).To(Equal(info("probe/unknown-version", path+" is not a regular file")))
	})

	It("refuses a file larger than 1 MiB", func() {
		path := put("VERSION", "quill 1.0.0\n"+strings.Repeat("x", 1<<20-11))
		_, f := read(quill(match))
		Expect(f).To(Equal(info("probe/unknown-version", path+" is larger than 1 MiB")))
		Expect(os.Truncate(path, 1<<20)).To(Succeed())
		Expect(read(quill(match))).To(Equal("1.0.0"), "exactly 1 MiB is read")
	})

	It("reports a file whose pattern finds nothing", func() {
		path := put("VERSION", "nothing here\n")
		_, f := read(quill(match))
		Expect(f).To(Equal(info("probe/unknown-version", path+" holds no version lola recognizes")))
	})

	It("reports a file that is not JSON", func() {
		path := put("state.json", "{not json")
		_, f := read(quill(pointer))
		Expect(f).To(HaveValue(And(HaveField("Severity", target.SeverityInfo), HaveField("Code", "probe/unknown-version"),
			HaveField("Text", HavePrefix(path+" is not JSON: invalid character")))))
	})

	It("reports a pointer to something other than a string, or to nothing", func() {
		path := put("state.json", `{"app": {"version": 1.4}}`)
		_, f := read(quill(pointer))
		Expect(f).To(Equal(info("probe/unknown-version", path+" has no version string at /app/version")))
		put("state.json", `{"app": ["1.4.0"]}`)
		_, f = read(quill(pointer))
		Expect(f).To(Equal(info("probe/unknown-version", path+" has no version string at /app/version")))
	})

	It("passes over a matched value that is not a version and tries the next method", func() {
		path := put("VERSION", "quill abc\n")
		odd := `file: {path: "{home}/.quill/VERSION", match: 'quill (\w+)'}`
		_, f := read(quill(odd))
		Expect(f).To(Equal(info("probe/unknown-version", path+` gave "abc", which is not a version`)))
		put("state.json", `{"app": {"version": "1.4.0"}}`)
		Expect(read(quill(odd, pointer))).To(Equal("1.4.0"))
	})

	It("does not cache a binary's output that is not a version", func() {
		binary("quill", "echo 'quill 1.2.x'\n")
		bin := filepath.Join(e.Paths.Search[0], "quill")
		_, f := read(quill(`exec: {binaries: [quill], args: [--version], match: 'quill (\S+)'}`))
		Expect(f).To(Equal(info("probe/unknown-version", bin+` --version gave "1.2.x", which is not a version`)))
		Expect(e.loadProbes()).To(BeEmpty())
	})
})
