//go:build unix

package main_test

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("a signal during a fetch", func() {
	DescribeTable("removes the run's staging directory and its lock, then dies of the signal, which a shell reports as 128 plus its number",
		func(sig syscall.Signal, code int) {
			tmp := testenv.Isolate()
			srv := filepath.Join(tmp, "srv")
			Expect(exec.Command("git", "init", "-q", "--bare", filepath.Join(srv, "kit.git")).Run()).To(Succeed())
			fetching, release := make(chan struct{}, 1), make(chan struct{})
			url := testenv.ServeGitWith(srv, func(r *http.Request) {
				select {
				case fetching <- struct{}{}:
				default:
				}
				select { // a slow remote: the fetch is in flight until lola gives up on it
				case <-release:
				case <-r.Context().Done():
				}
			}) + "/kit.git"
			DeferCleanup(func() { close(release) })

			session, err := gexec.Start(exec.Command(lolaBin, "info", url), GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(session.Kill)
			Eventually(fetching).WithTimeout(30 * time.Second).Should(Receive())
			staging := filepath.Join(os.Getenv("XDG_CACHE_HOME"), "lola", ".staging")
			Expect(filepath.Glob(filepath.Join(staging, "run-*"))).NotTo(BeEmpty(), "the fetch runs in its staging directory")

			session.Signal(sig)
			Eventually(session).WithTimeout(30 * time.Second).Should(gexec.Exit(code))
			status := session.Command.ProcessState.Sys().(syscall.WaitStatus)
			Expect(status.Signaled()).To(BeTrue(), "it dies of the signal once it has cleaned up, so a calling shell script stops too")
			Expect(status.Signal()).To(Equal(sig))
			Expect(filepath.Glob(filepath.Join(staging, "run-*"))).To(BeEmpty())
			Expect(string(session.Err.Contents())).To(BeEmpty(), "an interrupted run reports nothing, as a run the signal ended")
		},
		Entry("SIGINT (Ctrl+C): 130", syscall.SIGINT, 130),
		Entry("SIGTERM: 143", syscall.SIGTERM, 143),
	)

	It("is ignored when lola was started with SIGINT ignored, as a non-interactive shell starts a background job", func() {
		tmp := testenv.Isolate()
		srv := filepath.Join(tmp, "srv")
		Expect(exec.Command("git", "init", "-q", "--bare", filepath.Join(srv, "kit.git")).Run()).To(Succeed())
		fetching, release := make(chan struct{}, 1), make(chan struct{})
		url := testenv.ServeGitWith(srv, func(r *http.Request) {
			select {
			case fetching <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}) + "/kit.git"

		session, err := gexec.Start(exec.Command("sh", "-c", `trap '' INT; exec "$0" info "$1"`, lolaBin, url), GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(session.Kill)
		Eventually(fetching).WithTimeout(30 * time.Second).Should(Receive())
		session.Signal(syscall.SIGINT)
		Consistently(session).WithTimeout(time.Second).ShouldNot(gexec.Exit(), "the interrupt does not stop the fetch")
		close(release)
		Eventually(session).WithTimeout(30 * time.Second).Should(gexec.Exit())
		Expect(session.Command.ProcessState.Sys().(syscall.WaitStatus).Signaled()).To(BeFalse())
		Expect(session.ExitCode()).NotTo(Equal(130), string(session.Err.Contents()))
		Expect(string(session.Err.Contents())).NotTo(BeEmpty(), "the run ends as it would have without the interrupt, saying how")
	})
})
