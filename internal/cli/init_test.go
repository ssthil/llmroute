package cli

import (
	"bufio"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ssthil/llmroute/internal/config"
	"github.com/ssthil/llmroute/internal/database"
)

func TestParseSelection(t *testing.T) {
	cases := []struct {
		in       string
		n        int
		wantMode string
		wantSet  []int
	}{
		{"", 5, selKeep, nil},
		{"a", 5, selAll, nil},
		{"all", 5, selAll, nil},
		{"n", 5, selNone, nil},
		{"none", 5, selNone, nil},
		{"1,3,5", 5, selSubset, []int{1, 3, 5}},
		{" 2 , 4 ", 5, selSubset, []int{2, 4}},
		{"1,99,3", 5, selSubset, []int{1, 3}}, // out-of-range dropped
		{"x,2", 5, selSubset, []int{2}},       // non-numeric dropped
	}
	for _, tc := range cases {
		mode, set := parseSelection(tc.in, tc.n)
		if mode != tc.wantMode {
			t.Errorf("parseSelection(%q) mode = %q, want %q", tc.in, mode, tc.wantMode)
		}
		for _, i := range tc.wantSet {
			if !set[i] {
				t.Errorf("parseSelection(%q) missing %d", tc.in, i)
			}
		}
		if tc.wantSet != nil && len(set) != len(tc.wantSet) {
			t.Errorf("parseSelection(%q) set size = %d, want %d", tc.in, len(set), len(tc.wantSet))
		}
	}
}

func TestRunWizardSelectNone(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	db, err := database.Open(filepath.Join(t.TempDir(), "records.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// select none, then decline adding a custom provider.
	pio := &promptIO{
		in:     bufio.NewReader(strings.NewReader("n\nn\n")),
		out:    io.Discard,
		secret: func() (string, error) { return "", nil },
	}
	if err := runWizard(db, keysEmpty(), pio); err != nil {
		t.Fatalf("runWizard: %v", err)
	}
	all, _ := db.AllModels()
	for _, m := range all {
		if m.Enabled {
			t.Errorf("%s should be disabled after selecting none", m.Identifier)
		}
	}
}

func TestRunWizardSelectAllPromptsEachProviderKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	db, err := database.Open(filepath.Join(t.TempDir(), "records.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var secretCalls int
	pio := &promptIO{
		in:     bufio.NewReader(strings.NewReader("a\nn\n")), // all, then no custom
		out:    io.Discard,
		secret: func() (string, error) { secretCalls++; return "", nil },
	}
	if err := runWizard(db, keysEmpty(), pio); err != nil {
		t.Fatalf("runWizard: %v", err)
	}
	all, _ := db.AllModels()
	for _, m := range all {
		if !m.Enabled {
			t.Errorf("%s should be enabled after selecting all", m.Identifier)
		}
	}
	// One key prompt per distinct key-requiring provider (all 7 seeds need keys).
	provs, _ := db.AllProviders()
	if secretCalls != len(provs) {
		t.Errorf("key prompts = %d, want one per provider (%d)", secretCalls, len(provs))
	}
}

func keysEmpty() *config.Keys { return &config.Keys{Providers: map[string]string{}} }

func TestAddCustomProviderInteractive(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	db, err := database.Open(filepath.Join(t.TempDir(), "records.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// add groq (needs key, default env, cost 0.1), then decline a second one.
	answers := strings.Join([]string{
		"y",    // add a custom provider?
		"groq", // provider name
		"https://api.groq.com/openai/v1/chat/completions", // base URL
		"y",             // requires an API key?
		"",              // key env var -> default GROQ_API_KEY
		"llama-3.3-70b", // model id
		"chat,code",     // intents
		"0.1",           // cost
		"n",             // add another? no
	}, "\n") + "\n"

	keys := &config.Keys{Providers: map[string]string{}}
	pio := &promptIO{
		in:     bufio.NewReader(strings.NewReader(answers)),
		out:    io.Discard,
		secret: func() (string, error) { return "gsk-test-key", nil },
	}

	if err := addCustomProviders(db, keys, pio); err != nil {
		t.Fatalf("addCustomProviders: %v", err)
	}

	prov, err := db.Provider("groq")
	if err != nil {
		t.Fatalf("groq provider not created: %v", err)
	}
	if prov.BaseURL != "https://api.groq.com/openai/v1/chat/completions" || prov.KeyEnv != "GROQ_API_KEY" || !prov.NeedsKey {
		t.Errorf("groq provider = %+v", prov)
	}

	all, _ := db.AllModels()
	var found *database.Model
	for i := range all {
		if all[i].Identifier == "llama-3.3-70b" {
			found = &all[i]
		}
	}
	if found == nil {
		t.Fatal("llama-3.3-70b model not added")
	}
	if found.Provider != "groq" || found.IntentTags != "chat,code" || found.CostMultiplier != 0.1 || !found.Enabled {
		t.Errorf("model = %+v", *found)
	}
	if keys.Get("groq") != "gsk-test-key" {
		t.Errorf("groq key = %q", keys.Get("groq"))
	}
}

func TestAskYesNoDefaults(t *testing.T) {
	cases := []struct {
		line string
		def  bool
		want bool
	}{
		{"\n", true, true},
		{"\n", false, false},
		{"y\n", false, true},
		{"yes\n", false, true},
		{"n\n", true, false},
		{"no\n", true, false},
		{"garbage\n", true, true},
	}
	for _, tc := range cases {
		p := &promptIO{in: bufio.NewReader(strings.NewReader(tc.line)), out: io.Discard}
		got, err := askYesNo(p, "ok?", tc.def)
		if err != nil {
			t.Fatalf("askYesNo(%q): %v", tc.line, err)
		}
		if got != tc.want {
			t.Errorf("askYesNo(%q, def=%v) = %v, want %v", tc.line, tc.def, got, tc.want)
		}
	}
}
