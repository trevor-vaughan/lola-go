//go:build !windows

package yamldoc

import "syscall"

// oNoFollow makes opening a symbolic link fail.
const oNoFollow = syscall.O_NOFOLLOW
