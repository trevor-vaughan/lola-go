package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Digest hashes, for every regular file under dir in byte order of its slash path, the path,
// its mode and its content: path NUL octal(mode) NUL decimal(length) NUL content. The mode is
// 0755 for a path in executables (the source's own record, Fetched.Executables) and 0644
// otherwise; the file system's bits are never read, so a platform without execute bits
// digests the same content the same (Plan 6a §5.2). Symlinks are refused: cache content never
// contains them.
func Digest(dir string, executables []string) (string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s: symbolic links are not allowed in module content", p)
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	executable := make(map[string]bool, len(executables))
	for _, rel := range executables {
		executable[rel] = true
	}
	h := sha256.New()
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		mode := fs.FileMode(0o644)
		if executable[rel] {
			mode = 0o755
		}
		fmt.Fprintf(h, "%s\x00%o\x00%d\x00", rel, mode, len(data))
		h.Write(data)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
