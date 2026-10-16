package source

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"math/bits"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
)

// Archive limits (design §5). They are not configuration; specs lower them through fetch.
const (
	maxEntries = 20_000
	maxBytes   = 512 << 20 // total uncompressed bytes actually written
	maxRatio   = 200       // per zip entry: uncompressed size / compressed size
	maxBody    = 256 << 20 // a downloaded archive

	// maxDirBytesPerEntry bounds a zip's central directory to entries × this many bytes
	// before archive/zip reads it. A record is 46 bytes plus its name and extra fields,
	// usually under 200 bytes.
	maxDirBytesPerEntry = 1 << 10
)

// limits bound what one fetch may download and unpack.
type limits struct {
	entries int
	bytes   int64
	ratio   uint64
	body    int64
}

// rootMarkers locate an archive's module root (§3.1 rule 2): files, plus the directory
// .claude-plugin/.
var rootMarkers = map[string]bool{"lola.yaml": true, "lola.yml": true, ".lola.yaml": true, ".lola.yml": true, "plugin.json": true}

// contentDirs locate an archive's module root by their parent (§3.1 rule 3).
var contentDirs = map[string]bool{"skills": true, "commands": true, "agents": true, "rules": true}

// fetchArchive extracts the archive, downloaded first when it is a URL, into scratch, finds
// its module root and copies that with a directory walk.
func fetchArchive(ctx context.Context, spec Spec, scratch, stagingParent string, lim limits) (Fetched, error) {
	file, name := spec.Location, filepath.Base(spec.Location)
	if filepath.IsAbs(spec.Location) {
		fi, err := os.Lstat(file)
		if err != nil {
			return Fetched{}, fmt.Errorf("reading %s: %w", printable(file, maxName), cleanErr(err))
		}
		if !fi.Mode().IsRegular() {
			return Fetched{}, errs.WithHints(fmt.Errorf("%s is not a regular file", printable(file, maxName)),
				"point at a "+archiveSuffixList()+" file, not a link, pipe or device")
		}
	} else {
		u, err := parseURL(spec.Location)
		if err != nil {
			return Fetched{}, err
		}
		file, name = filepath.Join(scratch, "download"), path.Base(u.Path)
		if err := download(ctx, spec.Location, file, lim.body); err != nil {
			return Fetched{}, err
		}
	}
	tree := filepath.Join(scratch, "tree")
	if err := os.Mkdir(tree, 0o700); err != nil {
		return Fetched{}, err
	}
	if err := extract(file, archiveSuffix(name), tree, lim); err != nil {
		return Fetched{}, err
	}
	root, err := archiveRoot(tree, spec.Path)
	if err != nil {
		return Fetched{}, err
	}
	modName := filepath.Base(root)
	if root == tree {
		modName = name[:len(name)-len(archiveSuffix(name))]
	}
	dir, err := copyTree(ctx, root, stagingParent, listWalk)
	if err != nil {
		return Fetched{}, err
	}
	return Fetched{Dir: dir, Name: modName}, nil
}

// extractor writes archive entries below root, enforcing the entry rules and limits of §5.
type extractor struct {
	root    *os.Root
	lim     limits
	entries int
	written int64
}

// extract unpacks file, whose format suffix is suffix, into the empty directory dest.
func extract(file, suffix, dest string, lim limits) (err error) {
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	x := &extractor{root: root, lim: lim}
	if suffix == ".zip" {
		return x.zip(file)
	}
	return x.tar(file, suffix != ".tar")
}

func (x *extractor) zip(file string) (err error) {
	in, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("reading %s: %w", printable(file, maxName), cleanErr(err))
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	fi, err := in.Stat()
	if err != nil {
		return fmt.Errorf("reading %s: %w", printable(file, maxName), cleanErr(err))
	}
	if err := checkZipDirectory(in, fi.Size(), x.lim.entries); err != nil {
		return err
	}
	zr, err := zip.NewReader(in, fi.Size())
	if err != nil {
		return fmt.Errorf("reading %s: %w", printable(file, maxName), cleanErr(err))
	}
	for _, f := range zr.File {
		if err := x.count(); err != nil {
			return err
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			err = x.dir(f.Name)
		case mode.IsRegular():
			err = x.zipFile(f)
		default:
			err = refusedType(f.Name, mode.Type().String())
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// zip end-record layout (APPNOTE.TXT 4.3.14-4.3.16) and signatures.
const (
	zipEndLen      = 22
	zipEndSig      = 0x06054b50
	zip64LocLen    = 20
	zip64LocSig    = 0x07064b50
	zip64EndLen    = 56
	zip64EndSig    = 0x06064b50
	zipDirEntrySig = 0x02014b50
)

// checkZipDirectory refuses a zip whose central directory could list more than entries
// files, before archive/zip parses all of it into memory (design §5). It finds the end record
// (and the zip64 one) exactly as archive/zip does, then refuses a declared count over entries,
// or a declared size or actual span over entries × maxDirBytesPerEntry. The span matters:
// archive/zip reads records from the directory's start until one does not parse, whatever the
// declared size, and checks the count only modulo 65536. A file archive/zip would reject
// for its end record is left for it to reject.
func checkZipDirectory(r io.ReaderAt, size int64, entries int) error {
	le := binary.LittleEndian
	tail := make([]byte, min(size, 65<<10)) // archive/zip's search window
	if _, err := r.ReadAt(tail, size-int64(len(tail))); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("reading the zip's end record: %w", cleanErr(err))
	}
	at := -1
	for i := len(tail) - zipEndLen; i >= 0; i-- { // the last signature decides, as in archive/zip
		if le.Uint32(tail[i:]) == zipEndSig {
			if i+zipEndLen+int(le.Uint16(tail[i+20:])) <= len(tail) {
				at = i
			}
			break
		}
	}
	if at < 0 {
		return nil
	}
	end, endOffset := tail[at:], size-int64(len(tail))+int64(at)
	count, dirSize, dirOffset := uint64(le.Uint16(end[10:])), uint64(le.Uint32(end[12:])), uint64(le.Uint32(end[16:]))
	dirEnd := endOffset
	if count == 0xffff || dirSize == 0xffff || dirOffset == 0xffffffff { // archive/zip's zip64 test
		loc := make([]byte, zip64LocLen)
		if endOffset >= zip64LocLen {
			if _, err := r.ReadAt(loc, endOffset-zip64LocLen); err != nil {
				return nil
			}
		}
		// archive/zip keeps the legacy values when the locator is missing or its offset is negative.
		if p := le.Uint64(loc[8:]); le.Uint32(loc) == zip64LocSig && le.Uint32(loc[4:]) == 0 && le.Uint32(loc[16:]) == 1 && p <= math.MaxInt64 {
			dirEnd = int64(p)
			rec := make([]byte, zip64EndLen)
			if _, err := r.ReadAt(rec, dirEnd); err != nil || le.Uint32(rec) != zip64EndSig {
				return nil
			}
			count, dirSize, dirOffset = le.Uint64(rec[32:]), le.Uint64(rec[40:]), le.Uint64(rec[48:])
		}
	}
	if dirSize > math.MaxInt64 || dirOffset > math.MaxInt64 {
		return nil
	}
	base := dirEnd - int64(dirSize) - int64(dirOffset)
	start := base + int64(dirOffset)
	if start < 0 || start >= size {
		return nil
	}
	if base > 0 { // archive/zip then starts at dirOffset if a record parses there
		sig := make([]byte, 4)
		if _, err := r.ReadAt(sig, int64(dirOffset)); err == nil && le.Uint32(sig) == zipDirEntrySig {
			start = int64(dirOffset)
		}
	}
	limit := int64(entries) * maxDirBytesPerEntry
	hint := fmt.Sprintf("lola unpacks at most %d entries from an archive; package only the module's files", entries)
	switch span := max(int64(dirSize), endOffset-start); {
	case count > math.MaxInt64 || int64(count) > int64(entries):
		return errs.WithHints(fmt.Errorf("the zip's central directory declares %d entries, more than %d", count, entries), hint)
	case span > limit:
		return errs.WithHints(fmt.Errorf("the zip's central directory spans %d bytes, more than %d", span, limit), hint)
	}
	return nil
}

func (x *extractor) zipFile(f *zip.File) (err error) {
	if f.UncompressedSize64 > math.MaxInt64 {
		return fmt.Errorf("%s: declares an impossible size", printable(f.Name, maxName))
	}
	if hi, lo := bits.Mul64(f.CompressedSize64, x.lim.ratio); hi == 0 && f.UncompressedSize64 > lo {
		return fmt.Errorf("%s: compressed more than %d to 1; refusing a possible zip bomb", printable(f.Name, maxName), x.lim.ratio)
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("%s: %w", printable(f.Name, maxName), err)
	}
	defer func() { err = errors.Join(err, rc.Close()) }()
	return x.file(f.Name, f.Mode(), int64(f.UncompressedSize64), rc)
}

func (x *extractor) tar(file string, gz bool) (err error) {
	f, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("reading %s: %w", printable(file, maxName), cleanErr(err))
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	var r io.Reader = f
	if gz {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("reading %s: %w", printable(file, maxName), cleanErr(err))
		}
		defer func() { err = errors.Join(err, zr.Close()) }()
		r = zr
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading %s: %w", printable(file, maxName), cleanErr(err))
		}
		if err := x.count(); err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeXGlobalHeader: // archive-wide metadata, such as git archive's commit id
		case tar.TypeDir:
			err = x.dir(hdr.Name)
		case tar.TypeReg:
			err = x.file(hdr.Name, hdr.FileInfo().Mode(), hdr.Size, tr)
		default:
			err = refusedType(hdr.Name, fmt.Sprintf("type %q", hdr.Typeflag))
		}
		if err != nil {
			return err
		}
	}
}

func (x *extractor) count() error {
	x.entries++
	if x.entries > x.lim.entries {
		return fmt.Errorf("the archive has more than %d entries", x.lim.entries)
	}
	return nil
}

func refusedType(name, kind string) error {
	return fmt.Errorf("%s: only regular files and directories are allowed in an archive, not %s", printable(name, maxName), kind)
}

// entryPath returns the root-relative path of an entry, refusing any name that could
// resolve outside the extraction directory.
func entryPath(name string) (string, error) {
	if strings.Contains(name, `\`) || strings.HasPrefix(name, "/") || slices.Contains(strings.Split(name, "/"), "..") ||
		!filepath.IsLocal(name) {
		return "", fmt.Errorf("%s: unsafe path in the archive (absolute, '..', backslash or not local)", printable(name, maxName))
	}
	return path.Clean(name), nil
}

func (x *extractor) dir(name string) error {
	rel, err := entryPath(name)
	if err != nil || rel == "." {
		return err
	}
	if err := x.root.MkdirAll(rel, 0o755); err != nil {
		return fmt.Errorf("%s: %w", printable(name, maxName), cleanErr(err))
	}
	return nil
}

// file writes one regular entry of declared size from r. It never reads more than one byte
// past the declared size or the remaining total budget.
func (x *extractor) file(name string, mode fs.FileMode, size int64, r io.Reader) (err error) {
	rel, err := entryPath(name)
	if err != nil {
		return err
	}
	if dir := path.Dir(rel); dir != "." {
		if err := x.root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("%s: %w", printable(name, maxName), cleanErr(err))
		}
	}
	f, err := x.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fsx.NormalizeMode(mode))
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s: the archive holds this path more than once", printable(name, maxName))
	}
	if err != nil {
		return fmt.Errorf("%s: %w", printable(name, maxName), cleanErr(err))
	}
	n, err := io.Copy(f, io.LimitReader(r, min(size, x.lim.bytes-x.written)+1))
	x.written += n
	if err := errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("%s: %w", printable(name, maxName), cleanErr(err))
	}
	switch {
	case n > size:
		return fmt.Errorf("%s: longer than its header declares", printable(name, maxName))
	case x.written > x.lim.bytes:
		return fmt.Errorf("the archive unpacks to more than %d bytes", x.lim.bytes)
	}
	return nil
}

// archiveRoot finds the module root of the archive extracted at tree (design §3.1).
func archiveRoot(tree, subpath string) (string, error) {
	if subpath != "" {
		if !filepath.IsLocal(subpath) {
			return "", fmt.Errorf("--path %q must be a relative path inside the archive", subpath)
		}
		root := filepath.Join(tree, subpath)
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			return "", errs.WithHints(fmt.Errorf("--path %q is not a directory in the archive", subpath),
				"--path is relative to the archive's root, as tar tf or unzip -l show it")
		}
		return root, nil
	}
	markers, content := map[string]int{}, map[string]int{}
	err := filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == tree {
			return err
		}
		if ignoredNames[d.Name()] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		parent := filepath.Dir(p)
		depth := strings.Count(strings.TrimPrefix(parent, tree), string(filepath.Separator))
		switch {
		case !d.IsDir() && rootMarkers[d.Name()], d.IsDir() && d.Name() == ".claude-plugin":
			markers[parent] = depth
		case d.IsDir() && contentDirs[d.Name()]:
			content[parent] = depth
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	for _, cands := range []map[string]int{markers, content} {
		if root, err := shallowest(tree, cands); root != "" || err != nil {
			return root, err
		}
	}
	if entries, err := os.ReadDir(tree); err != nil {
		return "", err
	} else if len(entries) == 1 && entries[0].IsDir() {
		return filepath.Join(tree, entries[0].Name()), nil
	}
	return tree, nil
}

// shallowest returns the candidate directory of least depth, "" when there is none, and an
// error when two different candidates share that depth.
func shallowest(tree string, cands map[string]int) (string, error) {
	var best []string
	for dir, depth := range cands {
		switch {
		case len(best) == 0 || depth < cands[best[0]]:
			best = []string{dir}
		case depth == cands[best[0]]:
			best = append(best, dir)
		}
	}
	if len(best) <= 1 {
		return strings.Join(best, ""), nil
	}
	names := make([]string, len(best))
	for i, dir := range best {
		rel, err := filepath.Rel(tree, dir)
		if err != nil {
			return "", err
		}
		names[i] = printable(filepath.ToSlash(rel), maxName)
	}
	slices.Sort(names)
	return "", errs.WithHints(fmt.Errorf("the archive holds several module roots at the same depth: %s", strings.Join(names, ", ")),
		"choose one with --path, e.g. --path "+names[0])
}
