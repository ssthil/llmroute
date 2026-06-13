// Package router classifies inbound OpenAI-style chat payloads by intent,
// maps them onto the cheapest capable upstream provider, and transparently
// fails over to the next candidate when a provider is rate-limited or down.
package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/ssthil/llmroute/internal/database"
)

// Intent is a coarse classification of what a request is asking for.
type Intent string

const (
	IntentCode   Intent = "code"
	IntentVision Intent = "vision"
	IntentChat   Intent = "chat"
)

// Provider is re-exported from the database package so callers in this package
// read naturally. Endpoints live in the providers table, not in code.
type Provider = database.Provider

var (
	// codeRe matches fenced code blocks, common programming keywords, stack
	// traces, and source-file extensions.
	codeRe = regexp.MustCompile("(?is)```|\\bdef \\b|\\bfunc \\b|\\bclass \\b|\\bimport \\b|\\breturn\\b|\\bstack ?trace\\b|\\btraceback\\b|\\bexception\\b|\\bcompile\\b|\\balgorithm\\b|\\bnull ?pointer\\b|\\.(go|py|js|ts|rs|java|cpp|c|rb)\\b")
	// visionRe matches multi-modal schema markers, inline base64 assets, and
	// layout/visual keywords.
	visionRe = regexp.MustCompile(`(?is)"image_url"|"type"\s*:\s*"image|data:image/|data:application/|\bbase64\b|\bscreenshot\b|\bdiagram\b|\bocr\b|\bchart\b|\blayout\b`)
)

// Classify inspects a raw request body and returns its routing intent. Vision
// signals are checked first because their structural markers (image_url,
// data:image) are unambiguous, then code, then a chat default.
func Classify(body string) Intent {
	switch {
	case visionRe.MatchString(body):
		return IntentVision
	case codeRe.MatchString(body):
		return IntentCode
	default:
		return IntentChat
	}
}

// Router selects and dispatches to upstream providers.
type Router struct {
	db     *database.DB
	client *http.Client
	// keyFn resolves an environment variable; injectable for testing.
	keyFn func(env string) string
	// fileKeys holds keys persisted by interactive setup, indexed by provider
	// name. The environment takes precedence over these.
	fileKeys map[string]string
}

// New constructs a Router backed by db using the given HTTP client. A nil
// client falls back to http.DefaultClient.
func New(db *database.DB, client *http.Client) *Router {
	if client == nil {
		client = http.DefaultClient
	}
	return &Router{db: db, client: client, keyFn: os.Getenv}
}

// SetFileKeys registers provider keys loaded from the on-disk key store. They
// are used only when the corresponding environment variable is unset, so an
// exported env var always wins.
func (r *Router) SetFileKeys(keys map[string]string) {
	r.fileKeys = keys
}

// resolveKey returns the API key for a provider: the environment variable first
// (so an exported key overrides stored config), then the persisted key store.
func (r *Router) resolveKey(prov Provider) string {
	if v := r.keyFn(prov.KeyEnv); v != "" {
		return v
	}
	return r.fileKeys[prov.Name]
}

// Result carries a successful upstream response back to the caller.
type Result struct {
	Model    string
	Provider string
	Response *http.Response
}

// ErrNoUpstream is returned when every candidate model failed or had no key.
var ErrNoUpstream = errors.New("no upstream provider could satisfy the request")

// isRetryable reports whether an HTTP status warrants failover to the next
// candidate (rate limits and upstream outages).
func isRetryable(status int) bool {
	switch status {
	case http.StatusTooManyRequests, // 429
		http.StatusBadGateway,         // 502
		http.StatusServiceUnavailable, // 503
		http.StatusGatewayTimeout:     // 504
		return true
	default:
		return false
	}
}

// Dispatch classifies body, walks the cheapest-first candidate list for the
// detected intent, and returns the first provider that accepts the request.
// Providers with no configured API key are skipped. On a retryable upstream
// error it transparently moves to the next candidate.
func (r *Router) Dispatch(ctx context.Context, body []byte) (*Result, Intent, error) {
	intent := Classify(string(body))
	candidates, err := r.db.ModelsByIntent(string(intent))
	if err != nil {
		return nil, intent, err
	}
	providers, err := r.db.ProvidersMap()
	if err != nil {
		return nil, intent, err
	}

	var lastErr error
	// rateLimitErr retains the most recent retryable upstream error (429/5xx).
	// Tracked separately so a later "no API key" skip does not bury it in the
	// final error message — rate-limit context is more actionable to the caller.
	var rateLimitErr error
	for _, m := range candidates {
		prov, ok := providers[m.Provider]
		if !ok {
			lastErr = fmt.Errorf("model %q references unknown provider %q", m.Identifier, m.Provider)
			continue
		}

		// Local providers (needs_key=false) dispatch without an API key.
		var key string
		if prov.NeedsKey {
			key = r.resolveKey(prov)
			if key == "" {
				lastErr = fmt.Errorf("provider %q: no API key (set %s or run 'llmroute init')", prov.Name, prov.KeyEnv)
				continue
			}
		}

		upstreamBody, err := rewriteModel(body, m.Identifier)
		if err != nil {
			lastErr = err
			continue
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, prov.BaseURL, bytes.NewReader(upstreamBody))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}

		resp, err := r.client.Do(req)
		if err != nil {
			// Network-level failure: try the next candidate.
			lastErr = fmt.Errorf("provider %q transport error: %w", prov.Name, err)
			continue
		}
		if isRetryable(resp.StatusCode) {
			_ = resp.Body.Close()
			e := fmt.Errorf("provider %q returned retryable status %d", prov.Name, resp.StatusCode)
			lastErr = e
			rateLimitErr = e
			continue
		}
		return &Result{Model: m.Identifier, Provider: prov.Name, Response: resp}, intent, nil
	}

	if lastErr == nil {
		lastErr = ErrNoUpstream
	}
	// Prefer the retryable error over a "no API key" message: if a provider
	// was actually reached and rate-limited, that is the meaningful failure.
	if rateLimitErr != nil {
		lastErr = rateLimitErr
	}
	return nil, intent, fmt.Errorf("%w: %v", ErrNoUpstream, lastErr)
}

// rewriteModel replaces the "model" field of an OpenAI request body with the
// chosen upstream identifier, preserving the rest of the payload. Non-JSON or
// model-less bodies are returned unchanged.
func rewriteModel(body []byte, model string) ([]byte, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return body, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		// Forward opaque bodies untouched rather than failing the request.
		return body, nil
	}
	enc, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	payload["model"] = enc
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("re-encode request: %w", err)
	}
	return out, nil
}

// Usage mirrors the OpenAI usage accounting block.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// ParseUsage extracts the usage block from a buffered (non-streamed) upstream
// response body. Missing fields default to zero.
func ParseUsage(respBody []byte) Usage {
	var parsed struct {
		Usage Usage `json:"usage"`
	}
	_ = json.Unmarshal(respBody, &parsed)
	return parsed.Usage
}

// EstimateTokens gives a rough token count (~4 chars/token) for accounting when
// a provider omits usage data, e.g. on streamed responses.
func EstimateTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}
