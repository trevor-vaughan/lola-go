package fsx

import (
	"fmt"
	"strings"
)

// Pointer-block markers. lola owns the text between them; everything else in the file is the
// user's and is never touched.
const (
	BlockStart = "<!-- lola:instructions:start -->"
	BlockEnd   = "<!-- lola:instructions:end -->"
)

// findBlock returns the block's byte range; end includes the newline after BlockEnd if any.
func findBlock(c string) (start, end int, found bool, err error) {
	s, e := strings.Index(c, BlockStart), strings.Index(c, BlockEnd)
	if s < 0 && e < 0 {
		return 0, 0, false, nil
	}
	if s < 0 || e < 0 || e < s || strings.Count(c, BlockStart) > 1 || strings.Count(c, BlockEnd) > 1 {
		return 0, 0, false, fmt.Errorf("malformed lola instructions block: expect exactly one %q followed by one %q", BlockStart, BlockEnd)
	}
	end = e + len(BlockEnd)
	if end < len(c) && c[end] == '\n' {
		end++
	}
	return s, end, true, nil
}

// PointerBlock returns the current block text, markers included, without a trailing newline.
func PointerBlock(content []byte) (string, bool, error) {
	c := string(content)
	s, e, found, err := findBlock(c)
	if err != nil || !found {
		return "", false, err
	}
	return strings.TrimSuffix(c[s:e], "\n"), true, nil
}

// RenderPointerBlock renders the block for the given lines, markers included.
func RenderPointerBlock(lines []string) string {
	return BlockStart + "\n" + strings.Join(lines, "\n") + "\n" + BlockEnd
}

// SetPointerBlock replaces the block in place, appends it (after a blank line) when absent,
// or removes it, along with the blank separator line, when lines is empty.
func SetPointerBlock(content []byte, lines []string) ([]byte, error) {
	c := string(content)
	s, e, found, err := findBlock(c)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		if !found {
			return content, nil
		}
		before, after := c[:s], c[e:]
		if after == "" && strings.HasSuffix(before, "\n\n") {
			before = before[:len(before)-1]
		}
		return []byte(before + after), nil
	}
	block := RenderPointerBlock(lines) + "\n"
	switch {
	case found:
		return []byte(c[:s] + block + c[e:]), nil
	case c == "":
		return []byte(block), nil
	default:
		if !strings.HasSuffix(c, "\n") {
			c += "\n"
		}
		return []byte(c + "\n" + block), nil
	}
}
