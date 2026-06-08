package database

import (
	"path/filepath"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "records.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSeedIdempotent(t *testing.T) {
	db := openTestDB(t)

	first, err := db.AllModels()
	if err != nil {
		t.Fatalf("AllModels: %v", err)
	}
	if len(first) != len(seedModels) {
		t.Fatalf("seeded %d models, want %d", len(first), len(seedModels))
	}

	// Re-seeding must not duplicate rows.
	if err := db.Seed(); err != nil {
		t.Fatalf("re-Seed: %v", err)
	}
	second, err := db.AllModels()
	if err != nil {
		t.Fatalf("AllModels: %v", err)
	}
	if len(second) != len(first) {
		t.Errorf("after re-seed got %d models, want %d", len(second), len(first))
	}
}

func TestModelsByIntentOrdering(t *testing.T) {
	db := openTestDB(t)

	code, err := db.ModelsByIntent("code")
	if err != nil {
		t.Fatalf("ModelsByIntent: %v", err)
	}
	if len(code) == 0 {
		t.Fatal("expected code-capable models")
	}
	// Cheapest-first ordering.
	for i := 1; i < len(code); i++ {
		if code[i-1].CostMultiplier > code[i].CostMultiplier {
			t.Errorf("not cheapest-first: %v before %v", code[i-1], code[i])
		}
	}
	// Cheapest code model should be deepseek-reasoner (0.55) or deepseek-chat (0.14).
	if code[0].Provider != "deepseek" {
		t.Errorf("cheapest code provider = %q, want deepseek", code[0].Provider)
	}
}

func TestModelsByIntentUnknownFallsBackToAll(t *testing.T) {
	db := openTestDB(t)
	got, err := db.ModelsByIntent("nonsense-intent")
	if err != nil {
		t.Fatalf("ModelsByIntent: %v", err)
	}
	if len(got) != len(seedModels) {
		t.Errorf("fallback returned %d, want all %d", len(got), len(seedModels))
	}
}

func TestSetModelEnabledFiltersRouting(t *testing.T) {
	db := openTestDB(t)

	// All seeded models are enabled by default.
	all, _ := db.AllModels()
	enabled, _ := db.EnabledModels()
	if len(enabled) != len(all) {
		t.Fatalf("expected all %d models enabled, got %d", len(all), len(enabled))
	}

	// Disable the cheapest code model and confirm it drops out of routing.
	code, _ := db.ModelsByIntent("code")
	first := code[0].Identifier
	if err := db.SetModelEnabled(first, false); err != nil {
		t.Fatalf("SetModelEnabled: %v", err)
	}

	code2, _ := db.ModelsByIntent("code")
	for _, m := range code2 {
		if m.Identifier == first {
			t.Errorf("%q should be excluded after disabling", first)
		}
	}
	// AllModels still lists it, now disabled.
	for _, m := range mustAll(t, db) {
		if m.Identifier == first && m.Enabled {
			t.Errorf("%q should be marked disabled in AllModels", first)
		}
	}
}

func TestModelsByIntentFallbackRespectsEnabled(t *testing.T) {
	db := openTestDB(t)
	// Disable every model, then an unknown intent should fall back to the
	// enabled set — which is now empty.
	for _, m := range mustAll(t, db) {
		if err := db.SetModelEnabled(m.Identifier, false); err != nil {
			t.Fatalf("disable %s: %v", m.Identifier, err)
		}
	}
	got, err := db.ModelsByIntent("anything")
	if err != nil {
		t.Fatalf("ModelsByIntent: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no routable models, got %d", len(got))
	}
}

func mustAll(t *testing.T, db *DB) []Model {
	t.Helper()
	all, err := db.AllModels()
	if err != nil {
		t.Fatalf("AllModels: %v", err)
	}
	return all
}

func TestProvidersSeededAndCRUD(t *testing.T) {
	db := openTestDB(t)

	provs, err := db.AllProviders()
	if err != nil {
		t.Fatalf("AllProviders: %v", err)
	}
	if len(provs) != len(seedProviders) {
		t.Fatalf("seeded %d providers, want %d", len(provs), len(seedProviders))
	}
	if _, err := db.Provider("openai"); err != nil {
		t.Errorf("openai provider missing: %v", err)
	}
	if _, err := db.Provider("does-not-exist"); err == nil {
		t.Error("expected error for unknown provider")
	}

	// Add a local provider.
	local := Provider{Name: "ollama", BaseURL: "http://localhost:11434/v1/chat/completions", NeedsKey: false}
	if err := db.UpsertProvider(local); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	got, err := db.Provider("ollama")
	if err != nil {
		t.Fatalf("Provider(ollama): %v", err)
	}
	if got.NeedsKey || got.BaseURL != local.BaseURL {
		t.Errorf("ollama provider = %+v", got)
	}
}

func TestAddRemoveModel(t *testing.T) {
	db := openTestDB(t)

	// Adding a model for an unknown provider fails.
	if err := db.AddModel(Model{Provider: "ghost", Identifier: "x"}); err == nil {
		t.Error("expected error adding model with unknown provider")
	}

	_ = db.UpsertProvider(Provider{Name: "ollama", BaseURL: "http://localhost:11434/v1/chat/completions", NeedsKey: false})
	if err := db.AddModel(Model{Provider: "ollama", Identifier: "gemma3:27b", IntentTags: "chat,code"}); err != nil {
		t.Fatalf("AddModel: %v", err)
	}

	enabled, _ := db.ModelsByIntent("code")
	found := false
	for _, m := range enabled {
		if m.Identifier == "gemma3:27b" {
			found = true
		}
	}
	if !found {
		t.Error("gemma3:27b not routable for code after add")
	}

	// Re-adding an existing (disabled) model should re-enable and update it,
	// not fail on the unique identifier.
	if err := db.SetModelEnabled("gemma3:27b", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := db.AddModel(Model{Provider: "ollama", Identifier: "gemma3:27b", IntentTags: "chat", CostMultiplier: 0.05}); err != nil {
		t.Fatalf("re-AddModel should be idempotent: %v", err)
	}
	again, _ := db.AllModels()
	var readded bool
	for _, m := range again {
		if m.Identifier == "gemma3:27b" {
			readded = true
			if !m.Enabled {
				t.Error("re-added model should be enabled")
			}
			if m.IntentTags != "chat" || m.CostMultiplier != 0.05 {
				t.Errorf("re-added model not updated: %+v", m)
			}
		}
	}
	if !readded {
		t.Fatal("gemma3:27b missing after re-add")
	}

	if err := db.RemoveModel("gemma3:27b"); err != nil {
		t.Fatalf("RemoveModel: %v", err)
	}
	if err := db.RemoveModel("gemma3:27b"); err == nil {
		t.Error("expected error removing missing model")
	}
}

func TestLogUsageAndStats(t *testing.T) {
	db := openTestDB(t)

	if err := db.LogUsage("gpt-4o", 100, 50); err != nil {
		t.Fatalf("LogUsage: %v", err)
	}
	if err := db.LogUsage("gpt-4o", 20, 10); err != nil {
		t.Fatalf("LogUsage: %v", err)
	}
	if err := db.LogUsage("deepseek-chat", 5, 5); err != nil {
		t.Fatalf("LogUsage: %v", err)
	}

	stats, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("got %d stat rows, want 2", len(stats))
	}
	// Highest volume first: gpt-4o totals 180 tokens.
	if stats[0].Model != "gpt-4o" {
		t.Errorf("top model = %q, want gpt-4o", stats[0].Model)
	}
	if stats[0].Requests != 2 {
		t.Errorf("gpt-4o requests = %d, want 2", stats[0].Requests)
	}
	if stats[0].PromptTokens != 120 || stats[0].CompletionTokens != 60 {
		t.Errorf("gpt-4o tokens = %d/%d, want 120/60", stats[0].PromptTokens, stats[0].CompletionTokens)
	}
}
