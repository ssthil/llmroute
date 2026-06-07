package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadMissingReturnsEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	k, err := LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}
	if len(k.Providers) != 0 {
		t.Errorf("expected empty store, got %v", k.Providers)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	k, _ := LoadKeys()
	k.Set("openai", "sk-test-openai")
	k.Set("deepseek", "sk-test-deepseek")
	if err := k.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded, err := LoadKeys()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Get("openai") != "sk-test-openai" {
		t.Errorf("openai = %q", reloaded.Get("openai"))
	}
	if reloaded.Get("deepseek") != "sk-test-deepseek" {
		t.Errorf("deepseek = %q", reloaded.Get("deepseek"))
	}

	// File must be 0600 (POSIX only).
	if runtime.GOOS != "windows" {
		path, _ := KeysPath()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("keys file perm = %o, want 600", perm)
		}
	}
}

func TestSetEmptyClears(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	k, _ := LoadKeys()
	k.Set("openai", "sk-x")
	k.Set("openai", "")
	if k.Get("openai") != "" {
		t.Errorf("expected cleared key, got %q", k.Get("openai"))
	}
}

func TestKeysPathInConfigDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	path, err := KeysPath()
	if err != nil {
		t.Fatalf("KeysPath: %v", err)
	}
	if want := filepath.Join(tmp, "llmroute", KeysFileName); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}
