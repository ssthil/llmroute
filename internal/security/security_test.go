package security

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestScanForKeys(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantLeak bool
		provider string
	}{
		{"clean prompt", "summarize the quarterly report please", false, ""},
		{"openai key", `{"prompt":"my key is sk-abcdefghij1234567890ABCD"}`, true, "openai/deepseek"},
		{"anthropic key", "leak sk-ant-abcdefghij1234567890XYZ here", true, "anthropic"},
		{"google key", "token AIzaSyA1234567890abcdefghijklmnopqrstuv", true, "google"},
		{"aws key", "creds AKIAIOSFODNN7EXAMPLE rotated", true, "aws"},
		{"short sk no match", "sk-tooshort", false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ScanForKeys(tc.body)
			if tc.wantLeak {
				if err == nil {
					t.Fatalf("expected leak, got nil")
				}
				var le *LeakError
				if !errors.As(err, &le) {
					t.Fatalf("expected *LeakError, got %T", err)
				}
				if le.Provider != tc.provider {
					t.Errorf("provider = %q, want %q", le.Provider, tc.provider)
				}
			} else if err != nil {
				t.Fatalf("expected clean, got %v", err)
			}
		})
	}
}

func TestEnsureConfigDirPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	dir, err := EnsureConfigDir()
	if err != nil {
		t.Fatalf("EnsureConfigDir: %v", err)
	}
	if want := filepath.Join(tmp, appDir); dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != dirPerm {
		t.Errorf("dir perm = %o, want %o", perm, dirPerm)
	}
}

func TestOpenSecureFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	tmp := t.TempDir()
	path := filepath.Join(tmp, "secret.db")

	f, err := OpenSecureFile(path)
	if err != nil {
		t.Fatalf("OpenSecureFile: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != filePerm {
		t.Errorf("file perm = %o, want %o", perm, filePerm)
	}
}
