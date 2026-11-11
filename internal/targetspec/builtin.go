package targetspec

import "embed"

//go:embed specs/*.yaml
var builtin embed.FS

// Builtin parses the target specs lola ships, in file-name order.
func Builtin() ([]Spec, error) { return ParseFS(builtin, "specs", "builtin") }
