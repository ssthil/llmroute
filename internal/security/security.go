// Package security enforces strict local filesystem permissions and scans
// request payloads for leaked credential material before it leaves the host.
package security

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

const (
	// dirPerm locks the config directory to the owning user only.
	dirPerm os.FileMode = 0o700
	// filePerm locks individual files (db, keys) to the owning user only.
	filePerm os.FileMode = 0o600
)

// appDir is the directory name under the user config root.
const appDir = "llmroute"

// keyPatterns holds compiled signatures for common provider credentials. They
// are intentionally broad: the goal is to refuse to forward anything that even
// resembles a live secret to an upstream provider.
var keyPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	// Anthropic keys are prefixed sk-ant- and must be checked before the
	// generic OpenAI pattern so the more specific provider name wins.
	{"anthropic", regexp.MustCompile(`sk-ant-[a-zA-Z0-9_-]{20,}`)},
	// OpenAI / DeepSeek and most OpenAI-compatible providers share the
	// sk- prefix followed by a long alphanumeric body.
	{"openai/deepseek", regexp.MustCompile(`sk-[a-zA-Z0-9]{20,}`)},
	// Google AI Studio / Gemini keys.
	{"google", regexp.MustCompile(`AIza[a-zA-Z0-9_-]{30,}`)},
	// AWS access key IDs (Bedrock and friends).
	{"aws", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
}

// ConfigDir returns the absolute path to ~/.config/llmroute. It honors
// XDG_CONFIG_HOME when set, otherwise falls back to $HOME/.config so the path
// is identical across platforms. It does not touch the filesystem; use
// EnsureConfigDir to create it with locked-down perms.
func ConfigDir() (string, error) {
	root := os.Getenv("XDG_CONFIG_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		root = filepath.Join(home, ".config")
	}
	return filepath.Join(root, appDir), nil
}

// EnsureConfigDir creates ~/.config/llmroute (and parents) with 0700
// permissions if it does not already exist and returns its path. If the
// directory exists but is more permissive than 0700, it is tightened.
func EnsureConfigDir() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return "", fmt.Errorf("create config dir %q: %w", dir, err)
	}
	// MkdirAll respects umask, so re-assert the intended permissions.
	if err := os.Chmod(dir, dirPerm); err != nil {
		return "", fmt.Errorf("lock config dir %q: %w", dir, err)
	}
	return dir, nil
}

// OpenSecureFile opens (creating if necessary) a file for read/write using
// O_CREATE|O_RDWR with 0600 permissions so other local OS users cannot read
// its contents. Existing files have their mode re-asserted to 0600.
func OpenSecureFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, filePerm)
	if err != nil {
		return nil, fmt.Errorf("open secure file %q: %w", path, err)
	}
	if err := f.Chmod(filePerm); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock secure file %q: %w", path, err)
	}
	return f, nil
}

// LeakError reports which credential signature was detected in a payload.
type LeakError struct {
	Provider string
}

func (e *LeakError) Error() string {
	return fmt.Sprintf("request blocked: detected %s credential signature in payload", e.Provider)
}

// ScanForKeys inspects an arbitrary request body for credential signatures. It
// returns a *LeakError naming the matched provider if a secret is found, or nil
// when the body is clean. This is the gate called before any outbound network
// event triggers.
func ScanForKeys(body string) error {
	for _, p := range keyPatterns {
		if p.re.MatchString(body) {
			return &LeakError{Provider: p.name}
		}
	}
	return nil
}
