// Package config manages llmroute's on-disk configuration, notably the
// provider API keys captured during interactive setup. Keys are persisted to a
// 0600 file inside the locked-down config directory so other local OS users
// cannot read them.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ssthil/llmroute/internal/security"
)

// KeysFileName is the on-disk name of the provider key store.
const KeysFileName = "keys.json"

// Keys holds provider API keys, indexed by provider name (e.g. "openai").
type Keys struct {
	Providers map[string]string `json:"providers"`
}

// KeysPath returns the canonical keys.json location inside the config dir,
// creating the directory (0700) if needed.
func KeysPath() (string, error) {
	dir, err := security.EnsureConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, KeysFileName), nil
}

// LoadKeys reads the key store. A missing file is not an error: it returns an
// empty, ready-to-use Keys so first-run flows can populate it.
func LoadKeys() (*Keys, error) {
	path, err := KeysPath()
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Keys{Providers: map[string]string{}}, nil
		}
		return nil, fmt.Errorf("open keys file %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read keys file %q: %w", path, err)
	}

	k := &Keys{Providers: map[string]string{}}
	if len(data) > 0 {
		if err := json.Unmarshal(data, k); err != nil {
			return nil, fmt.Errorf("parse keys file %q: %w", path, err)
		}
	}
	if k.Providers == nil {
		k.Providers = map[string]string{}
	}
	return k, nil
}

// Save writes the key store to disk with strict 0600 permissions. The file is
// truncated and rewritten on each save.
func (k *Keys) Save() error {
	path, err := KeysPath()
	if err != nil {
		return err
	}

	f, err := security.OpenSecureFile(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncate keys file %q: %w", path, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind keys file %q: %w", path, err)
	}

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(k); err != nil {
		return fmt.Errorf("write keys file %q: %w", path, err)
	}
	return nil
}

// Get returns the stored key for a provider, or "" if none is set.
func (k *Keys) Get(provider string) string {
	if k == nil || k.Providers == nil {
		return ""
	}
	return k.Providers[provider]
}

// Set records (or clears, when value is empty) a provider key in memory. Call
// Save to persist.
func (k *Keys) Set(provider, value string) {
	if k.Providers == nil {
		k.Providers = map[string]string{}
	}
	if value == "" {
		delete(k.Providers, provider)
		return
	}
	k.Providers[provider] = value
}
