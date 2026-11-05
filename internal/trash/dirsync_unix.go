//go:build !windows

package trash

import (
	"errors"
	"os"
)

// syncDir flushes a directory's entries to disk, so a file or link created in it survives a crash.
func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
