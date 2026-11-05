package native_test

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"os"
	"path/filepath"
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/trevor-vaughan/lola-go/internal/native"
	"github.com/trevor-vaughan/lola-go/internal/native/nativetest"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("nativetest fixtures", func() {
	BeforeEach(func() { testenv.Isolate() })
	It("are accepted by the stdlib parsers", func() {
		for _, data := range [][]byte{nativetest.ELF64(), nativetest.ELF64Dyn()} {
			f, err := elf.NewFile(bytes.NewReader(data))
			Expect(err).NotTo(HaveOccurred())
			Expect(f.Close()).To(Succeed())
		}
		_, err := macho.NewFile(bytes.NewReader(nativetest.MachO64()))
		Expect(err).NotTo(HaveOccurred())
		ff, err := macho.NewFatFile(bytes.NewReader(nativetest.MachOFat()))
		Expect(err).NotTo(HaveOccurred())
		Expect(ff.Arches).To(HaveLen(1))
		_, err = pe.NewFile(bytes.NewReader(nativetest.PE64()))
		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("Find", func() {
	var root *os.Root
	var dir string
	put := func(rel string, data []byte) {
		GinkgoHelper()
		Expect(os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, rel), data, 0o644)).To(Succeed())
	}
	find := func(files ...string) []native.Binary {
		GinkgoHelper()
		out, err := native.Find(root, files)
		Expect(err).NotTo(HaveOccurred())
		return out
	}
	BeforeEach(func() {
		testenv.Isolate()
		dir = GinkgoT().TempDir()
		var err error
		root, err = os.OpenRoot(dir)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(root.Close)
	})
	DescribeTable("recognises",
		func(data []byte, f native.Format, kind string, arch []string, os string) {
			put("x", data)
			got := find("x")
			Expect(got).To(HaveLen(1))
			Expect(got[0]).To(MatchFields(IgnoreExtras, Fields{"Path": Equal("x"), "Format": Equal(f), "Kind": Equal(kind),
				"Arch": Equal(arch), "OS": Equal(os), "Malformed": BeFalse()}))
		},
		Entry("ELF executable", nativetest.ELF64(), native.Format("ELF"), "executable", []string{"x86-64"}, "Unix-like"),
		Entry("ELF PIE", nativetest.ELF64Dyn(), native.Format("ELF"), "executable", []string{"x86-64"}, "Unix-like"),
		Entry("Mach-O", nativetest.MachO64(), native.Format("Mach-O"), "executable", []string{"arm64"}, "Apple platforms"),
		Entry("fat Mach-O", nativetest.MachOFat(), native.Format("Mach-O"), "executable", []string{"x86-64"}, "Apple platforms"),
		Entry("PE", nativetest.PE64(), native.Format("PE"), "executable", []string{"x86-64"}, "Windows"),
	)
	DescribeTable("does not report",
		func(data []byte) { put("x", data); Expect(find("x")).To(BeEmpty()) },
		Entry("a Java class file", nativetest.JavaClass()),
		Entry("text starting MZ", []byte("MZ is the sound of a long and quiet sentence that goes on for sixty-four bytes or more.....")),
		Entry("a short text file", []byte("#!/bin/sh\necho hi\n")),
		Entry("an empty file", []byte{}),
		Entry("one byte", []byte{0x7f}),
		Entry("a PNG", append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, make([]byte, 56)...)),
	)
	It("reports a truncated ELF as malformed, naming the format it imitates", func() {
		put("x", nativetest.ELF64()[:20])
		got := find("x")
		Expect(got).To(HaveLen(1))
		Expect(got[0].Format).To(Equal(native.Format("ELF")))
		Expect(got[0].Malformed).To(BeTrue())
	})
	It("returns results sorted by path and skips non-binaries among them", func() {
		put("b/two", nativetest.ELF64())
		put("a/one", nativetest.PE64())
		put("c/text", []byte("hi"))
		got := find("b/two", "a/one", "c/text")
		Expect([]string{got[0].Path, got[1].Path}).To(Equal([]string{"a/one", "b/two"}))
	})
	It("recognises this test program itself, a Go binary, for the platform's format", func() {
		self, err := os.Executable()
		Expect(err).NotTo(HaveOccurred())
		data, err := os.ReadFile(self)
		Expect(err).NotTo(HaveOccurred())
		put("self", data)
		want := map[string]native.Format{"linux": "ELF", "freebsd": "ELF", "darwin": "Mach-O", "windows": "PE"}[runtime.GOOS]
		Expect(find("self")).To(ConsistOf(HaveField("Format", want)))
	})
	It("returns an error naming the file when one cannot be opened", func() {
		_, err := native.Find(root, []string{"missing"})
		Expect(err).To(MatchError(ContainSubstring("missing")))
	})
})
