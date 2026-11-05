//go:build windows

package trust

import "io/fs"

// writableByOthers is false on Windows, where permission bits do not describe access; only the
// symbolic link check applies.
func writableByOthers(fs.FileInfo) bool { return false }
