package output

import (
	"os"

	"golang.org/x/term"
)

// ColorMode is the --color setting.
type ColorMode string

const (
	ColorAuto   ColorMode = "auto"
	ColorAlways ColorMode = "always"
	ColorNever  ColorMode = "never"
)

// UseColor decides per stream, so `kcctl ... | jq` keeps colored errors on
// the terminal while stdout stays plain.
func UseColor(mode ColorMode, f *os.File) bool {
	switch mode {
	case ColorAlways:
		return true
	case ColorNever:
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if v := os.Getenv("FORCE_COLOR"); v != "" && v != "0" {
		return true
	}
	if v := os.Getenv("CLICOLOR_FORCE"); v != "" && v != "0" {
		return true
	}
	if os.Getenv("CLICOLOR") == "0" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return f != nil && term.IsTerminal(int(f.Fd()))
}

// Style uses only the 16 named ANSI colors so terminal themes stay in charge.
type Style struct{ on bool }

func NewStyle(on bool) Style { return Style{on: on} }

func (s Style) wrap(code, txt string) string {
	if !s.on || txt == "" {
		return txt
	}
	return "\x1b[" + code + "m" + txt + "\x1b[0m"
}

func (s Style) Bold(t string) string    { return s.wrap("1", t) }
func (s Style) Dim(t string) string     { return s.wrap("2", t) }
func (s Style) Red(t string) string     { return s.wrap("31", t) }
func (s Style) Green(t string) string   { return s.wrap("32", t) }
func (s Style) Yellow(t string) string  { return s.wrap("33", t) }
func (s Style) Cyan(t string) string    { return s.wrap("36", t) }
func (s Style) BoldRed(t string) string { return s.wrap("1;31", t) }
func (s Style) Enabled() bool           { return s.on }
