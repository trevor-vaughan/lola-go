package source_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// procStat is the fields of /proc/<pid>/stat after the command name: state first, session id fourth.
func procStat(raw string) []string {
	_, rest, ok := strings.Cut(raw, ") ")
	Expect(ok).To(BeTrue(), raw)
	return strings.Fields(rest)
}

var _ = Describe("RemoteTip over ssh", func() {
	var tmp string
	BeforeEach(func() { tmp = testenv.Isolate() })
	fakeSSH := func(body string) {
		script := filepath.Join(tmp, "fake-ssh")
		Expect(os.WriteFile(script, []byte("#!/bin/sh\n"+body), 0o700)).To(Succeed())
		GinkgoT().Setenv("GIT_SSH_COMMAND", script)
	}
	spec := source.Spec{Kind: source.KindGit, Location: "ssh://example.invalid/r.git"}

	It("gives ssh no terminal and no askpass program to prompt with", func() {
		GinkgoT().Setenv("SSH_ASKPASS_REQUIRE", "force")
		fakeSSH(`cat /proc/$$/stat > "` + tmp + `/stat"
if (exec 3<>/dev/tty) 2>/dev/null; then echo opened; else echo refused; fi > "` + tmp + `/tty"
echo "$SSH_ASKPASS_REQUIRE" > "` + tmp + `/askpass"
exit 255
`)
		_, err := source.RemoteTip(context.Background(), spec)
		Expect(err).To(HaveOccurred())
		Expect(os.ReadFile(filepath.Join(tmp, "tty"))).To(BeEquivalentTo("refused\n"))
		stat, err := os.ReadFile(filepath.Join(tmp, "stat"))
		Expect(err).NotTo(HaveOccurred())
		ours, err := os.ReadFile("/proc/self/stat")
		Expect(err).NotTo(HaveOccurred())
		Expect(procStat(string(stat))[3]).NotTo(Equal(procStat(string(ours))[3]), "ssh ran in lola's session, which may own the terminal")
		Expect(os.ReadFile(filepath.Join(tmp, "askpass"))).To(BeEquivalentTo("never\n"))
	})

	It("stops the ssh git started once the deadline passes", func() {
		fakeSSH(`echo $$ > "` + tmp + `/pid"
exec sleep 30
`)
		short, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := source.RemoteTip(short, spec)
		Expect(err).To(HaveOccurred())
		Expect(time.Since(start)).To(BeNumerically("<", 3*time.Second))
		raw, err := os.ReadFile(filepath.Join(tmp, "pid"))
		Expect(err).NotTo(HaveOccurred())
		pid := strings.TrimSpace(string(raw))
		Eventually(func() bool {
			stat, err := os.ReadFile("/proc/" + pid + "/stat")
			return err != nil || procStat(string(stat))[0] == "Z" // gone, or dead and not yet reaped
		}).WithTimeout(2 * time.Second).Should(BeTrue())
	})
})
