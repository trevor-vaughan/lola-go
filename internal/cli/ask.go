package cli

import (
	"strings"
	"unicode"
)

// answerWords are the words an answer letter stands for: typed in their place in accessible mode,
// and named for Enter's default in the hint and the key line.
var answerWords = map[rune]string{'y': "yes", 'n': "no", 'c': "change"}

// parseAnswer reads input as an answer to a question taking answers ("yN", see Question): input
// trimmed, empty is the default (not ok when there is none); one of the letters, or its word
// (yes, no, change), in either case, is that letter, lowercased; anything else is not ok.
func parseAnswer(input, answers string) (rune, bool) {
	input = strings.ToLower(strings.TrimSpace(input))
	for _, r := range answers {
		l := unicode.ToLower(r)
		switch {
		case input == "" && unicode.IsUpper(r):
			return l, true
		case input != "" && (input == string(l) || input == answerWords[l]):
			return l, true
		}
	}
	return 0, false
}

// answerBrackets is answers as the prompt shows them: "[y/N]", "[y/c/n]".
func answerBrackets(answers string) string {
	return "[" + strings.Join(strings.Split(answers, ""), "/") + "]"
}

// answerHint is what the prompt says to a key or a line it does not take: the letters, and what
// Enter alone means when there is a default: "answer y or n (Enter means no)", "answer y, c or n".
func answerHint(answers string) string {
	var letters []string
	def := ""
	for _, r := range answers {
		l := unicode.ToLower(r)
		if unicode.IsUpper(r) {
			def = answerWords[l]
		}
		letters = append(letters, string(l))
	}
	hint := "answer " + strings.Join(letters[:len(letters)-1], ", ") + " or " + letters[len(letters)-1]
	if def != "" {
		hint += " (Enter means " + def + ")"
	}
	return hint
}
