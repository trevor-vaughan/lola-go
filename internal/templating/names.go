package templating

// PartialsDir is the directory, at a module's root, that holds its partials (Plan 7 §3.3).
const PartialsDir = "_partials"

// PartialName reports whether name is a partial's name: a lowercase letter or digit, then up
// to 63 lowercase letters, digits, '_' and '-'.
func PartialName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i := range len(name) {
		switch c := name[i]; {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}
