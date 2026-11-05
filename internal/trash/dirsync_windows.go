//go:build windows

package trash

// syncDir does nothing: Windows cannot sync a directory.
func syncDir(string) error { return nil }
