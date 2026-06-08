package cli

import (
	"io"
	"os"

	"golang.org/x/term"
)

// readSecret reads a single line from stdin without echoing it (for API keys).
func readSecret() (string, error) {
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	return string(b), err
}

// isTerminal reports whether r is an interactive terminal.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// maskKey renders an API key for display without revealing it, e.g. sk-…7890.
func maskKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:3] + "…" + key[len(key)-4:]
}
