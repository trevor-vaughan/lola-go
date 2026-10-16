package source

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("archive limits", func() {
	var tmp, staging string
	BeforeEach(func() {
		tmp = testenv.Isolate()
		staging = filepath.Join(tmp, "staging")
	})
	roomy := limits{entries: 100, bytes: 1 << 20, ratio: 200}
	fetchWith := func(archive string, lim limits) error {
		_, err := fetch(context.Background(), Spec{Kind: KindArchive, Location: archive}, staging, lim)
		return err
	}
	writeTarFiles := func(path string, files map[string]string) {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for name, body := range files {
			Expect(tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(body))})).To(Succeed())
			_, err := tw.Write([]byte(body))
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(tw.Close()).To(Succeed())
		Expect(os.WriteFile(path, buf.Bytes(), 0o644)).To(Succeed())
	}

	It("refuses more entries than the limit", func() {
		archive := filepath.Join(tmp, "many.tar")
		writeTarFiles(archive, map[string]string{"a.md": "", "b.md": "", "c.md": ""})
		Expect(fetchWith(archive, roomy)).To(Succeed())
		lim := roomy
		lim.entries = 2
		Expect(fetchWith(archive, lim)).To(MatchError("the archive has more than 2 entries"))
	})

	It("refuses more bytes in total than the limit, counted as written", func() {
		archive := filepath.Join(tmp, "big.tar")
		writeTarFiles(archive, map[string]string{"a.md": "123456", "b.md": "123456"})
		lim := roomy
		lim.bytes = 12
		Expect(fetchWith(archive, lim)).To(Succeed())
		lim.bytes = 11
		Expect(fetchWith(archive, lim)).To(MatchError("the archive unpacks to more than 11 bytes"))
		entries, _ := os.ReadDir(staging)
		Expect(entries).To(HaveLen(1)) // the earlier successful fetch only
	})

	It("refuses a zip entry compressed beyond the ratio", func() {
		archive := filepath.Join(tmp, "bomb.zip")
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, err := zw.Create("a.md")
		Expect(err).NotTo(HaveOccurred())
		var body strings.Builder
		for i := range 2000 {
			body.WriteString(strconv.Itoa(i * 7919))
		}
		_, err = w.Write([]byte(body.String()))
		Expect(err).NotTo(HaveOccurred())
		Expect(zw.Close()).To(Succeed())
		Expect(os.WriteFile(archive, buf.Bytes(), 0o644)).To(Succeed())
		Expect(fetchWith(archive, roomy)).To(Succeed())
		lim := roomy
		lim.ratio = 1
		Expect(fetchWith(archive, lim)).To(MatchError("a.md: compressed more than 1 to 1; refusing a possible zip bomb"))
	})
})

var _ = Describe("zip central directory bound", func() {
	var tmp string
	BeforeEach(func() { tmp = testenv.Isolate() })
	// zipOf returns a stored zip of n empty files with 60-byte names: about 106 bytes of
	// central directory each.
	zipOf := func(n int) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for i := range n {
			_, err := zw.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("%057d.md", i), Method: zip.Store})
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(zw.Close()).To(Succeed())
		return buf.Bytes()
	}
	fetchZip := func(b []byte, entries int) error {
		archive := filepath.Join(tmp, "d.zip")
		Expect(os.WriteFile(archive, b, 0o644)).To(Succeed())
		_, err := fetch(context.Background(), Spec{Kind: KindArchive, Location: archive}, filepath.Join(tmp, "s"),
			limits{entries: entries, bytes: 1 << 20, ratio: 200})
		return err
	}
	le := binary.LittleEndian

	It("refuses a declared entry count over the limit before archive/zip reads the directory", func() {
		err := fetchZip(zipOf(3), 2)
		Expect(err).To(MatchError("the zip's central directory declares 3 entries, more than 2"))
		Expect(errs.Hints(err)).NotTo(BeEmpty())
	})

	It("refuses a directory larger than the limit allows even when its end record understates it", func() {
		b := zipOf(200) // about 21 KiB of directory, over 10 entries × 1 KiB
		end := b[len(b)-22:]
		le.PutUint16(end[8:], 1)  // entries on this disk
		le.PutUint16(end[10:], 1) // entries in total
		le.PutUint32(end[12:], 0) // directory size
		err := fetchZip(b, 10)
		Expect(err).To(MatchError(MatchRegexp(`^the zip's central directory spans \d+ bytes, more than 10240$`)))
		Expect(errs.Hints(err)).NotTo(BeEmpty())
	})

	// zip64Of rewrites zipOf(3) with a zip64 end record that declares a million entries, found
	// through a locator that points at offset (the record's real offset when -1).
	zip64Of := func(offset int64) []byte {
		b := zipOf(3)
		end := append([]byte(nil), b[len(b)-22:]...)
		at := uint64(len(b) - 22)
		rec := make([]byte, 56)
		le.PutUint32(rec[0:], 0x06064b50)
		le.PutUint64(rec[4:], 44)
		le.PutUint64(rec[24:], 1_000_000)                   // entries on this disk
		le.PutUint64(rec[32:], 1_000_000)                   // entries in total
		le.PutUint64(rec[40:], uint64(le.Uint32(end[12:]))) // directory size
		le.PutUint64(rec[48:], uint64(le.Uint32(end[16:]))) // directory offset
		loc := make([]byte, 20)
		le.PutUint32(loc[0:], 0x07064b50)
		le.PutUint64(loc[8:], at)
		if offset != -1 {
			le.PutUint64(loc[8:], uint64(offset))
		}
		le.PutUint32(loc[16:], 1)
		le.PutUint16(end[8:], 0xffff)
		le.PutUint16(end[10:], 0xffff)
		return append(append(append(b[:at:at], rec...), loc...), end...)
	}

	It("reads the zip64 end record as archive/zip does", func() {
		Expect(fetchZip(zip64Of(-1), 100)).To(MatchError("the zip's central directory declares 1000000 entries, more than 100"))
	})

	It("keeps the legacy end record, as archive/zip does, when the zip64 locator's offset is negative", func() {
		Expect(fetchZip(zip64Of(math.MinInt64), 100)).To(MatchError("the zip's central directory declares 65535 entries, more than 100"))
	})

	It("accepts a directory within the limit", func() {
		Expect(fetchZip(zipOf(10), 10)).To(Succeed())
	})
})

var _ = Describe("archive entry counting", func() {
	It("counts a tar global header toward the entry limit", func() {
		tmp := testenv.Isolate()
		archive := filepath.Join(tmp, "g.tar")
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		Expect(tw.WriteHeader(&tar.Header{Name: "g", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "c"}})).To(Succeed())
		Expect(tw.WriteHeader(&tar.Header{Name: "a.md", Mode: 0o644, Typeflag: tar.TypeReg})).To(Succeed())
		Expect(tw.Close()).To(Succeed())
		Expect(os.WriteFile(archive, buf.Bytes(), 0o644)).To(Succeed())
		spec := Spec{Kind: KindArchive, Location: archive}
		_, err := fetch(context.Background(), spec, filepath.Join(tmp, "s"), limits{entries: 2, bytes: 100, ratio: 200})
		Expect(err).NotTo(HaveOccurred())
		_, err = fetch(context.Background(), spec, filepath.Join(tmp, "s"), limits{entries: 1, bytes: 100, ratio: 200})
		Expect(err).To(MatchError("the archive has more than 1 entries"))
	})
})
