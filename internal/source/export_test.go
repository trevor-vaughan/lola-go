package source

import (
	"errors"
	"os"
)

// ResolveLinkForTest resolves the link at linkRel, a slash path below boundary, as copyFiles does.
func ResolveLinkForTest(boundary, linkRel string) (target string, err error) {
	root, err := os.OpenRoot(boundary)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	target, _, err = resolveLink(root, ".", linkRel)
	return target, err
}
