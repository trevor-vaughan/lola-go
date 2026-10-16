package errs

import (
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("shell quoting per OS", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("quotes for the shell of the OS lola runs on", func() {
		args := []string{"lola", "install", "./my mod", "it's", "-t", "a,b", ""}
		Expect(ShellJoin(args)).To(Equal(shellJoin(runtime.GOOS, args)))
	})

	It("quotes for a POSIX shell off Windows", func() {
		for _, goos := range []string{"linux", "darwin"} {
			Expect(shellJoin(goos, []string{"lola", "install", "./my mod", "it's", "--user", ""})).
				To(Equal(`lola install './my mod' 'it'\''s' --user ''`))
			Expect(shellJoin(goos, []string{"-t", "claude,cursor", "@a", "x=1", "50%", "a:b"})).
				To(Equal(`-t claude,cursor @a x=1 50% a:b`), "POSIX gives these characters no meaning")
			Expect(shellJoin(goos, []string{"$x", "`id`", "a;b", "a&b", "a|b", "ü", "‘"})).
				To(Equal("'$x' '`id`' 'a;b' 'a&b' 'a|b' 'ü' '‘'"))
		}
	})

	It("quotes for PowerShell on Windows", func() {
		q := func(args ...string) string { return shellJoin("windows", args) }
		Expect(q("lola", "install", "./src/my-mod", "--scope", "project", "-C", "C:/x_y/z.1", "k=v", "+a")).
			To(Equal("lola install ./src/my-mod --scope project -C C:/x_y/z.1 k=v +a"))
		Expect(q("./my mod", "")).To(Equal(`'./my mod' ''`))
		Expect(q("it's")).To(Equal(`'it''s'`))
		Expect(q("$x", "`id`", "a;b", "a&b", "a|b", `a"b`, `C:\x`, "(x)", "{x}", "<x", "#x")).
			To(Equal("'$x' '`id`' 'a;b' 'a&b' 'a|b' 'a\"b' 'C:\\x' '(x)' '{x}' '<x' '#x'"))
		Expect(q("@a", "a,b")).To(Equal(`'@a' 'a,b'`), "splatting and the array operator")
		Expect(q("--%", "50%")).To(Equal(`'--%' '50%'`), "the stop-parsing token")
		Expect(q("-a.b", "--ref=v1.2", "a.b", "-ab")).To(Equal(`'-a.b' '--ref=v1.2' a.b -ab`),
			"PowerShell splits a bare argument that starts with - at its first .")
		Expect(q("ü", "a\tb", "a\nb")).To(Equal("'ü' 'a\tb' 'a\nb'"))
	})

	It("doubles every character PowerShell reads as a single quote", func() {
		for _, sq := range []string{"'", "\u2018", "\u2019", "\u201A", "\u201B"} {
			Expect(shellJoin("windows", []string{"a" + sq + "b"})).To(Equal("'a" + sq + sq + "b'"))
			Expect(shellJoin("windows", []string{sq})).To(Equal("'" + sq + sq + "'"))
		}
		Expect(shellJoin("windows", []string{"\u2019; rm x; \u2018"})).
			To(Equal("'\u2019\u2019; rm x; \u2018\u2018'"), "a smart quote cannot close the string")
	})
})
