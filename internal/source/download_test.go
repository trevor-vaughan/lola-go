package source_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Fetch of an archive URL", func() {
	var tmp, staging string
	var body []byte
	var hits atomic.Int32
	ctx := context.Background()
	BeforeEach(func() {
		tmp = testenv.Isolate()
		staging = filepath.Join(tmp, "staging")
		archive := filepath.Join(tmp, "src.tar.gz")
		writeTar(archive, true, file("commands/a.md", "A"))
		var err error
		body, err = os.ReadFile(archive)
		Expect(err).NotTo(HaveOccurred())
		hits.Store(0)
	})
	serve := func(h http.HandlerFunc) string {
		srv := httptest.NewServer(h)
		DeferCleanup(srv.Close)
		return srv.URL
	}
	fetch := func(url string) (source.Fetched, error) {
		return source.Fetch(ctx, source.Spec{Kind: source.KindArchive, Location: url}, staging)
	}
	expectNothingStaged := func() {
		entries, _ := os.ReadDir(staging)
		Expect(entries).To(BeEmpty())
	}

	It("downloads, extracts and names the module after the URL's file", func() {
		url := serve(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })
		f, err := fetch(url + "/dl/example-1.0.tar.gz")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Name).To(Equal("example-1.0"))
		Expect(listTree(f.Dir)).To(ConsistOf("commands/a.md"))
		Expect(filepath.Dir(f.Dir)).To(Equal(staging))
		entries, _ := os.ReadDir(staging)
		Expect(entries).To(HaveLen(1)) // the download and scratch tree are gone
	})

	It("downloads again on every fetch", func() {
		url := serve(func(w http.ResponseWriter, r *http.Request) {
			n := hits.Add(1)
			archive := filepath.Join(tmp, fmt.Sprintf("v%d.tar.gz", n))
			writeTar(archive, true, file("commands/a.md", "version "+strconv.Itoa(int(n))))
			http.ServeFile(w, r, archive)
		})
		for want := range []int{1, 2} {
			f, err := fetch(url + "/m.tar.gz")
			Expect(err).NotTo(HaveOccurred())
			Expect(os.ReadFile(filepath.Join(f.Dir, "commands/a.md"))).To(BeEquivalentTo(fmt.Sprintf("version %d", want+1)))
		}
		Expect(hits.Load()).To(BeEquivalentTo(2))
	})

	It("fails on a status other than 200, naming it", func() {
		url := serve(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
		_, err := fetch(url + "/m.zip")
		Expect(err).To(MatchError(ContainSubstring("the server answered 404 Not Found")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("check the URL")))
		expectNothingStaged()
	})

	It("follows at most 5 redirects", func() {
		url := serve(func(w http.ResponseWriter, r *http.Request) {
			n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/r"), ".tgz"))
			if n > 0 {
				http.Redirect(w, r, fmt.Sprintf("/r%d.tgz", n-1), http.StatusFound)
				return
			}
			_, _ = w.Write(body)
		})
		_, err := fetch(url + "/r5.tgz")
		Expect(err).NotTo(HaveOccurred())
		_, err = fetch(url + "/r6.tgz")
		Expect(err).To(MatchError(ContainSubstring("stopped after 5 redirects")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("final URL")))
	})

	It("coaches a refused redirect to plain http", func() {
		url := serve(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://example.invalid/m.tgz", http.StatusFound)
		})
		_, err := fetch(url + "/m.tgz")
		Expect(err).To(MatchError(ContainSubstring("not loopback")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("final https:// URL")))
	})

	It("coaches a download that does not finish in time, before or during the body", func() {
		for _, headersFirst := range []bool{false, true} {
			url := serve(func(w http.ResponseWriter, r *http.Request) {
				if headersFirst {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("x"))
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			})
			short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			_, err := source.Fetch(short, source.Spec{Kind: source.KindArchive, Location: url + "/m.tgz"}, staging)
			cancel()
			Expect(err).To(HaveOccurred(), "headers first: %v", headersFirst)
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("in time")), "headers first: %v", headersFirst)
		}
	})

	It("keeps a redirect target's query out of the error", func() {
		url := serve(func(w http.ResponseWriter, r *http.Request) {
			n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/r"), ".tgz"))
			http.Redirect(w, r, fmt.Sprintf("/r%d.tgz?sig=s3cr3t", n+1), http.StatusFound)
		})
		_, err := fetch(url + "/r0.tgz")
		Expect(err).To(MatchError(ContainSubstring("stopped after 5 redirects")))
		Expect(err.Error()).NotTo(ContainSubstring("s3cr3t"))
	})

	It("refuses a credential or query URL handed straight to Fetch, without echoing it", func() {
		for _, loc := range []string{"https://tok3n@example.com/m.zip", "https://example.com/m.zip?sig=s3cr3t", "https://example.com/m.zip#s3cr3t"} {
			_, err := fetch(loc)
			Expect(err).To(HaveOccurred(), loc)
			Expect(err.Error()).NotTo(SatisfyAny(ContainSubstring("tok3n"), ContainSubstring("s3cr3t")), loc)
		}
		expectNothingStaged()
	})

	It("does not decode a gzip Content-Encoding", func() {
		url := serve(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(body)
		})
		f, err := fetch(url + "/m.tar.gz")
		Expect(err).NotTo(HaveOccurred())
		Expect(listTree(f.Dir)).To(ConsistOf("commands/a.md"))
	})

	It("sanitizes the status text a server sends", func() {
		url := serve(func(w http.ResponseWriter, r *http.Request) {
			hj, ok := w.(http.Hijacker)
			Expect(ok).To(BeTrue())
			conn, buf, err := hj.Hijack()
			Expect(err).NotTo(HaveOccurred())
			_, _ = buf.WriteString("HTTP/1.1 500 Bad\x1b[2J\r\nContent-Length: 0\r\n\r\n")
			_ = buf.Flush()
			_ = conn.Close()
		})
		_, err := fetch(url + "/m.zip")
		Expect(err).To(MatchError(ContainSubstring("the server answered 500 Bad?[2J")))
	})
})
