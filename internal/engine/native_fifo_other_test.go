//go:build !unix

package engine

import "errors"

func mkfifoForTest(string) error { return errors.New("FIFOs need a Unix system") }
