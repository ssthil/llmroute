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

func TestRunWizardSelectionAndKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	db, err := database.Open(filepath.Join(t.TempDir(), "records.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Providers are walked alphabetically: anthropic, deepseek, gemini, openai.
	// Within each, models are cheapest-first.
	//   anthropic: haiku, sonnet   -> n, n        (no key)
	//   deepseek:  chat,  reasoner -> y, n        (key #1)
	//   gemini:    flash, pro      -> n, n        (no key)
	//   openai:    gpt-4o          -> y           (key #2)
	answers := "n\nn\n" + "y\nn\n" + "n\nn\n" + "y\n"

	secrets := []string{"sk-deepseek-key", "sk-openai-key"}
	var si int
	keys := &config.Keys{Providers: map[string]string{}}

	pio := &promptIO{
		in:  bufio.NewReader(strings.NewReader(answers)),
		out: io.Discard,
		secret: func() (string, error) {
			s := secrets[si]
			si++
			return s, nil
		},
	}

	if err := runWizard(db, keys, pio); err != nil {
		t.Fatalf("runWizard: %v", err)
	}

	// Expected enabled state.
	want := map[string]bool{
		"claude-3-5-haiku":  false,
		"claude-3-5-sonnet": false,
		"deepseek-chat":     true,
		"deepseek-reasoner": false,
		"gemini-2.5-flash":  false,
		"gemini-2.5-pro":    false,
		"gpt-4o":            true,
	}
	all, _ := db.AllModels()
	for _, m := range all {
		if w, ok := want[m.Identifier]; ok && m.Enabled != w {
			t.Errorf("%s enabled = %v, want %v", m.Identifier, m.Enabled, w)
		}
	}

	if keys.Get("deepseek") != "sk-deepseek-key" {
		t.Errorf("deepseek key = %q", keys.Get("deepseek"))
	}
	if keys.Get("openai") != "sk-openai-key" {
		t.Errorf("openai key = %q", keys.Get("openai"))
	}
	if keys.Get("anthropic") != "" || keys.Get("gemini") != "" {
		t.Errorf("unexpected keys stored for disabled providers: %v", keys.Providers)
	}
	if si != 2 {
		t.Errorf("expected exactly 2 key prompts, got %d", si)
	}
}

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
