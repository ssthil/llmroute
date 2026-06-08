package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// colorEnabled controls ANSI styling. It honors the NO_COLOR convention and a
// dumb terminal, and only colors when stdout is an interactive terminal.
var colorEnabled = detectColor()

func detectColor() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiBlue   = "\x1b[34m"
	ansiCyan   = "\x1b[36m"
	ansiGray   = "\x1b[90m"
)

func paint(code, s string) string {
	if !colorEnabled {
		return s
	}
	return code + s + ansiReset
}

func bold(s string) string   { return paint(ansiBold, s) }
func dim(s string) string    { return paint(ansiDim, s) }
func red(s string) string    { return paint(ansiRed, s) }
func green(s string) string  { return paint(ansiGreen, s) }
func yellow(s string) string { return paint(ansiYellow, s) }
func cyan(s string) string   { return paint(ansiCyan, s) }
func gray(s string) string   { return paint(ansiGray, s) }

// Glyphs used across the UI.
const (
	glyphOK    = "✓"
	glyphOff   = "○"
	glyphArrow = "→"
	glyphDot   = "•"
	glyphWarn  = "▲"
	glyphErr   = "✗"
)

// header prints a bold title with a dim underline rule, e.g.:
//
//	llmroute · setup
//	────────────────
func header(w io.Writer, title string) {
	t := "llmroute " + dim("·") + " " + bold(title)
	fmt.Fprintln(w, t)
	fmt.Fprintln(w, gray(strings.Repeat("─", len("llmroute · "+title))))
}

// field prints an aligned "label : value" line with a leading check.
func field(w io.Writer, label, value string) {
	fmt.Fprintf(w, "%s %s %s\n", green(glyphOK), dim(fmt.Sprintf("%-10s", label)), value)
}

func success(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "%s %s\n", green(glyphOK), fmt.Sprintf(format, a...))
}

func info(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "%s %s\n", cyan(glyphArrow), fmt.Sprintf(format, a...))
}

func warn(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "%s %s\n", yellow(glyphWarn), fmt.Sprintf(format, a...))
}

func note(w io.Writer, format string, a ...any) {
	fmt.Fprintln(w, gray("  "+fmt.Sprintf(format, a...)))
}
