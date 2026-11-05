//go:build windows

package migrate

// syncDir does nothing: Windows cannot sync a directory.
func syncDir(string) error { return nil }
