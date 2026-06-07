// Package database is a zero-CGO SQLite coordinator built on modernc.org/sqlite.
// It owns schema migrations, baseline model seeding, and usage accounting for
// the llmroute proxy.
package database

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	// Pure-Go SQLite driver. Registers itself as "sqlite".
	_ "modernc.org/sqlite"

	"github.com/ssthil/llmroute/internal/security"
)

// DBFileName is the on-disk name of the embedded database.
const DBFileName = "records.db"

// DB wraps a *sql.DB connection pool against the local SQLite file.
type DB struct {
	*sql.DB
}

// Model is a routable upstream entry as stored in the models table.
type Model struct {
	ID             int64
	Provider       string  // e.g. "openai", "anthropic", "gemini", "deepseek"
	Identifier     string  // upstream model id, e.g. "gpt-4o"
	CostMultiplier float64 // relative token cost weight
	IntentTags     string  // comma separated, e.g. "code,chat"
	Enabled        bool    // whether the router may route to this model
}

// StatRow is an aggregated per-model usage summary.
type StatRow struct {
	Model            string
	Requests         int64
	PromptTokens     int64
	CompletionTokens int64
}

// DefaultPath returns the canonical database location inside the locked-down
// config directory, creating the directory if needed.
func DefaultPath() (string, error) {
	dir, err := security.EnsureConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, DBFileName), nil
}

// Open opens (and locks down) the SQLite database at path, applies migrations,
// and seeds baseline models. The file is created with 0600 permissions.
func Open(path string) (*DB, error) {
	// Pre-create the file through the security layer so the on-disk handle is
	// guaranteed to carry 0600 before the driver ever touches it.
	f, err := security.OpenSecureFile(path)
	if err != nil {
		return nil, err
	}
	_ = f.Close()

	// busy_timeout avoids spurious "database is locked" errors under
	// concurrent proxy logging; foreign_keys keeps referential rules on.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)", path)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	// SQLite tolerates a single writer; keep the pool small and predictable.
	sqlDB.SetMaxOpenConns(1)

	db := &DB{sqlDB}
	if err := db.Migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := db.Seed(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS models (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    provider        TEXT    NOT NULL,
    identifier      TEXT    NOT NULL UNIQUE,
    cost_multiplier REAL    NOT NULL DEFAULT 1.0,
    intent_tags     TEXT    NOT NULL DEFAULT 'chat',
    enabled         INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS usage_logs (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at        DATETIME NOT NULL,
    model             TEXT     NOT NULL,
    prompt_tokens     INTEGER  NOT NULL DEFAULT 0,
    completion_tokens INTEGER  NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_usage_logs_model ON usage_logs(model);
`

// Migrate creates the models and usage_logs schemas if they do not yet exist
// and applies additive column migrations for databases created by earlier
// versions.
func (db *DB) Migrate() error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	// enabled was introduced after the initial release; add it to pre-existing
	// model tables. SQLite reports a duplicate-column error when it already
	// exists, which we treat as a no-op.
	if _, err := db.Exec(`ALTER TABLE models ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate models.enabled: %w", err)
	}
	return nil
}

// seedModels is the baseline routing matrix inserted on first boot.
var seedModels = []Model{
	{Provider: "gemini", Identifier: "gemini-2.5-flash", CostMultiplier: 0.30, IntentTags: "vision,chat"},
	{Provider: "gemini", Identifier: "gemini-2.5-pro", CostMultiplier: 1.00, IntentTags: "vision,chat,code"},
	{Provider: "anthropic", Identifier: "claude-3-5-sonnet", CostMultiplier: 3.00, IntentTags: "code,chat"},
	{Provider: "anthropic", Identifier: "claude-3-5-haiku", CostMultiplier: 0.80, IntentTags: "chat"},
	{Provider: "openai", Identifier: "gpt-4o", CostMultiplier: 2.50, IntentTags: "vision,chat,code"},
	{Provider: "deepseek", Identifier: "deepseek-chat", CostMultiplier: 0.14, IntentTags: "chat,code"},
	{Provider: "deepseek", Identifier: "deepseek-reasoner", CostMultiplier: 0.55, IntentTags: "code"},
}

// Seed inserts the baseline models. It is idempotent: existing identifiers are
// left untouched thanks to INSERT OR IGNORE on the unique identifier column.
func (db *DB) Seed() error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin seed tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO models
		(provider, identifier, cost_multiplier, intent_tags) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare seed: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, m := range seedModels {
		if _, err := stmt.Exec(m.Provider, m.Identifier, m.CostMultiplier, m.IntentTags); err != nil {
			return fmt.Errorf("seed model %q: %w", m.Identifier, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit seed: %w", err)
	}
	return nil
}

// ModelsByIntent returns enabled models whose intent_tags contain the given
// intent, ordered cheapest-first so the failover loop tries low-cost models
// before escalating. When no enabled model matches the intent, every enabled
// model is returned so the router always has a candidate to fall back on.
func (db *DB) ModelsByIntent(intent string) ([]Model, error) {
	rows, err := db.Query(`
		SELECT id, provider, identifier, cost_multiplier, intent_tags, enabled
		FROM models
		WHERE enabled = 1 AND ',' || intent_tags || ',' LIKE '%,' || ? || ',%'
		ORDER BY cost_multiplier ASC`, intent)
	if err != nil {
		return nil, fmt.Errorf("query models by intent %q: %w", intent, err)
	}
	defer func() { _ = rows.Close() }()

	models, err := scanModels(rows)
	if err != nil {
		return nil, err
	}
	if len(models) > 0 {
		return models, nil
	}
	return db.EnabledModels()
}

// AllModels returns every model (enabled or not) ordered cheapest-first. Use it
// for display; routing should use ModelsByIntent / EnabledModels.
func (db *DB) AllModels() ([]Model, error) {
	return db.queryModels(`
		SELECT id, provider, identifier, cost_multiplier, intent_tags, enabled
		FROM models ORDER BY cost_multiplier ASC`)
}

// EnabledModels returns only enabled models, ordered cheapest-first.
func (db *DB) EnabledModels() ([]Model, error) {
	return db.queryModels(`
		SELECT id, provider, identifier, cost_multiplier, intent_tags, enabled
		FROM models WHERE enabled = 1 ORDER BY cost_multiplier ASC`)
}

func (db *DB) queryModels(query string, args ...any) ([]Model, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query models: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanModels(rows)
}

// SetModelEnabled toggles whether the router may route to a model.
func (db *DB) SetModelEnabled(identifier string, enabled bool) error {
	if _, err := db.Exec(`UPDATE models SET enabled = ? WHERE identifier = ?`, boolToInt(enabled), identifier); err != nil {
		return fmt.Errorf("set enabled for %q: %w", identifier, err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func scanModels(rows *sql.Rows) ([]Model, error) {
	var out []Model
	for rows.Next() {
		var m Model
		var enabled int
		if err := rows.Scan(&m.ID, &m.Provider, &m.Identifier, &m.CostMultiplier, &m.IntentTags, &enabled); err != nil {
			return nil, fmt.Errorf("scan model row: %w", err)
		}
		m.Enabled = enabled != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// LogUsage appends a usage record for a completed upstream call.
func (db *DB) LogUsage(model string, promptTokens, completionTokens int) error {
	_, err := db.Exec(`
		INSERT INTO usage_logs (created_at, model, prompt_tokens, completion_tokens)
		VALUES (?, ?, ?, ?)`,
		time.Now().UTC(), model, promptTokens, completionTokens)
	if err != nil {
		return fmt.Errorf("log usage for %q: %w", model, err)
	}
	return nil
}

// Stats returns per-model aggregated usage ordered by total token volume.
func (db *DB) Stats() ([]StatRow, error) {
	rows, err := db.Query(`
		SELECT model,
		       COUNT(*)                       AS requests,
		       COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
		       COALESCE(SUM(completion_tokens), 0) AS completion_tokens
		FROM usage_logs
		GROUP BY model
		ORDER BY (SUM(prompt_tokens) + SUM(completion_tokens)) DESC`)
	if err != nil {
		return nil, fmt.Errorf("query stats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []StatRow
	for rows.Next() {
		var s StatRow
		if err := rows.Scan(&s.Model, &s.Requests, &s.PromptTokens, &s.CompletionTokens); err != nil {
			return nil, fmt.Errorf("scan stat row: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
