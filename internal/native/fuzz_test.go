package native_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trevor-vaughan/lola-go/internal/native"
	"github.com/trevor-vaughan/lola-go/internal/native/nativetest"
)

// FuzzFind feeds arbitrary bytes to the detector and the describer; neither may panic or stall.
func FuzzFind(f *testing.F) {
	for _, fx := range [][]byte{nativetest.ELF64(), nativetest.ELF64Dyn(), nativetest.MachO64(),
		nativetest.MachOFat(), nativetest.PE64(), nativetest.JavaClass()} {
		for n := 0; n <= len(fx); n++ {
			f.Add(fx[:n])
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "x"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		start := time.Now()
		if _, err := native.Find(root, []string{"x"}); err != nil {
			t.Fatalf("Find: %v", err)
		}
		_, _ = native.Describe(root, "x")
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("took %v", d)
		}
	})
}
