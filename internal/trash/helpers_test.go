package trash_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/gomega"
)

// writeFile creates path with content, making its parent directories.
func writeFile(path, content string) {
	Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
	Expect(os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
}

// writeTree creates files under root.
func writeTree(root string, files map[string]string) {
	for p, c := range files {
		writeFile(filepath.Join(root, filepath.FromSlash(p)), c)
	}
}
