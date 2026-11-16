package cli

import (
	"bytes"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("folds", func() {
	BeforeEach(func() { testenv.Isolate() })
	detail := []string{"create claude-code a", "create claude-code b"}
	footer := "→ " + foldFooter + "\n"

	It("print the summary alone, marked ▸, into a plain writer, and the footer when asked", func() {
		var b bytes.Buffer
		printFold(&b, style{}, "kit: 2 changes", detail)
		printFoldFooter(&b, style{})
		Expect(b.String()).To(Equal("▸ kit: 2 changes\n" + footer))
	})

	It("print the summary and every detail line under -v and --plain", func() {
		for _, s := range []style{{verbose: true}, {plain: true}} {
			var b bytes.Buffer
			printFold(&b, s, "kit: 2 changes", detail)
			Expect(b.String()).To(Equal("kit: 2 changes\n    create claude-code a\n    create claude-code b\n"))
		}
	})

	It("are held by a reportBuf, whose Folded prints them closed and the footer once, where the last was put", func() {
		var r reportBuf
		fmt.Fprintln(&r, "Files:")
		printFold(&r, style{}, "kit: 2 changes", detail)
		printFoldFooter(&r, style{})
		printFold(&r, style{}, "docs: 1 change", detail[:1])
		printFoldFooter(&r, style{})
		fmt.Fprintln(&r, "→ to apply: lola sync")
		Expect(r.folds()).To(Equal(2))
		Expect(r.Folded()).To(Equal("Files:\n▸ kit: 2 changes\n▸ docs: 1 change\n" + footer + "→ to apply: lola sync\n"))
	})

	It("print no footer when no fold is closed", func() {
		var r reportBuf
		printFold(&r, style{verbose: true}, "kit: 2 changes", detail)
		printFoldFooter(&r, style{})
		Expect(r.folds()).To(BeZero())
		Expect(r.Folded()).NotTo(ContainSubstring(foldFooter))
	})

	It("keep their folds and add the outer indent through ReplayTo into another reportBuf", func() {
		inner := reportBuf{Indent: "  "}
		fmt.Fprintln(&inner, "Notices:")
		printFold(&inner, style{}, "kit: 2 changes", detail)
		outer := reportBuf{Indent: "> "}
		inner.ReplayTo(&outer)
		Expect(outer.folds()).To(Equal(1))
		Expect(outer.Folded()).To(Equal(">   Notices:\n>   ▸ kit: 2 changes\n"))
		var plain bytes.Buffer
		inner.ReplayTo(&plain)
		Expect(plain.String()).To(Equal(inner.Folded()))
	})

	It("render open folds marked ▾ with their detail, the footer only while one is still closed", func() {
		var r reportBuf
		printFold(&r, style{}, "kit: 2 changes", detail)
		printFold(&r, style{}, "docs: 1 change", detail[:1])
		r.FoldFooter("F")
		Expect(joinLines(renderLines(r.blocks, []bool{true, false}))).To(Equal(
			"▾ kit: 2 changes\n    create claude-code a\n    create claude-code b\n▸ docs: 1 change\nF\n"))
		Expect(joinLines(renderLines(r.blocks, []bool{true, true}))).NotTo(ContainSubstring("F\n"))
	})

	It("is empty when nil", func() {
		var r *reportBuf
		Expect(r.Folded()).To(BeEmpty())
	})
})
