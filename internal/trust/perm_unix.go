//go:build !windows

package trust

import "io/fs"

// writableByOthers reports whether the group or anyone may write fi.
func writableByOthers(fi fs.FileInfo) bool { return fi.Mode().Perm()&0o022 != 0 }
