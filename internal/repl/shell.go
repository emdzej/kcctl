package repl

import "strings"

// Word is one shell word with its source span, so the prompt can mark or
// replace exactly what the user typed.
type Word struct {
	Text       string
	Start, End int // byte offsets in the line
}

// Split tokenizes like POSIX sh (single quotes literal, double quotes with
// backslash escapes, backslash outside quotes). incomplete reports an open
// quote or trailing backslash, which continues the line.
func Split(line string) (words []Word, incomplete bool) {
	var b strings.Builder
	inWord := false
	start := 0
	var quote byte
	flush := func(end int) {
		if inWord {
			words = append(words, Word{Text: b.String(), Start: start, End: end})
			b.Reset()
			inWord = false
		}
	}
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case quote == '\'':
			if ch == '\'' {
				quote = 0
			} else {
				b.WriteByte(ch)
			}
		case quote == '"':
			switch {
			case ch == '"':
				quote = 0
			case ch == '\\' && i+1 < len(line) && strings.IndexByte("\"\\$`\n", line[i+1]) >= 0:
				i++
				b.WriteByte(line[i])
			case ch == '\\' && i+1 == len(line):
				return append(words, Word{Text: b.String(), Start: start, End: i}), true
			default:
				b.WriteByte(ch)
			}
		case ch == '\\':
			if i+1 == len(line) {
				flush(i)
				return words, true
			}
			if !inWord {
				inWord, start = true, i
			}
			i++
			if line[i] != '\n' {
				b.WriteByte(line[i])
			}
		case ch == ' ' || ch == '\t' || ch == '\n':
			flush(i)
		case ch == '\'' || ch == '"':
			if !inWord {
				inWord, start = true, i
			}
			quote = ch
		default:
			if !inWord {
				inWord, start = true, i
			}
			b.WriteByte(ch)
		}
	}
	if quote != 0 {
		flush(len(line))
		return words, true
	}
	flush(len(line))
	return words, false
}

// Texts returns the word texts.
func Texts(ws []Word) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Text
	}
	return out
}

// Quote returns s quoted for the line if it needs it.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.ContainsAny(s, " \t\n'\"\\$`*?[]{}()<>|&;#~") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}
