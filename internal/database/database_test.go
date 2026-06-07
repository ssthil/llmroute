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
