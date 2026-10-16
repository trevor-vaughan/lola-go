package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("download", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("refuses a redirect from https to http, and allows https to https", func() {
		from := httptest.NewRequest(http.MethodGet, "https://example.com/m.zip", nil)
		Expect(checkRedirect(httptest.NewRequest(http.MethodGet, "http://example.com/m.zip", nil), []*http.Request{from})).
			To(MatchError("refusing a redirect from https to http"))
		Expect(checkRedirect(httptest.NewRequest(http.MethodGet, "https://cdn.example.com/m.zip", nil), []*http.Request{from})).To(Succeed())
	})

	It("refuses a redirect to plain http on a host that is not loopback, and allows loopback", func() {
		from := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/m.zip", nil)
		Expect(checkRedirect(httptest.NewRequest(http.MethodGet, "http://example.com/m.zip", nil), []*http.Request{from})).
			To(MatchError(ContainSubstring("not loopback")))
		Expect(checkRedirect(httptest.NewRequest(http.MethodGet, "http://localhost:9/m.zip", nil), []*http.Request{from})).To(Succeed())
	})

	It("refuses a body over the cap", func() {
		tmp := testenv.Isolate()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", 11)))
		}))
		DeferCleanup(srv.Close)
		Expect(download(context.Background(), srv.URL+"/m.zip", filepath.Join(tmp, "a"), 11)).To(Succeed())
		_, err := fetch(context.Background(), Spec{Kind: KindArchive, Location: srv.URL + "/m.zip"}, filepath.Join(tmp, "staging"),
			limits{entries: 10, bytes: 1 << 20, ratio: 200, body: 10})
		Expect(err).To(MatchError(ContainSubstring("the archive is larger than 10 bytes")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("install the file")))
	})
})
