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

// Provider describes how to reach an upstream OpenAI-compatible endpoint.
type Provider struct {
	Name    string // matches database.Model.Provider
	BaseURL string // full chat-completions URL
	KeyEnv  string // environment variable holding the bearer token
}

// Providers is the registry of supported upstreams. Each exposes an
// OpenAI-compatible /chat/completions surface.
var Providers = map[string]Provider{
	"openai":    {Name: "openai", BaseURL: "https://api.openai.com/v1/chat/completions", KeyEnv: "OPENAI_API_KEY"},
	"gemini":    {Name: "gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions", KeyEnv: "GEMINI_API_KEY"},
	"anthropic": {Name: "anthropic", BaseURL: "https://api.anthropic.com/v1/chat/completions", KeyEnv: "ANTHROPIC_API_KEY"},
	"deepseek":  {Name: "deepseek", BaseURL: "https://api.deepseek.com/v1/chat/completions", KeyEnv: "DEEPSEEK_API_KEY"},
}

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
	// keyFn resolves a provider's API key; injectable for testing.
	keyFn func(env string) string
}

// New constructs a Router backed by db using the given HTTP client. A nil
// client falls back to http.DefaultClient.
func New(db *database.DB, client *http.Client) *Router {
	if client == nil {
		client = http.DefaultClient
	}
	return &Router{db: db, client: client, keyFn: os.Getenv}
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

	var lastErr error
	for _, m := range candidates {
		prov, ok := Providers[m.Provider]
		if !ok {
			continue
		}
		key := r.keyFn(prov.KeyEnv)
		if key == "" {
			lastErr = fmt.Errorf("provider %q: %s not set", prov.Name, prov.KeyEnv)
			continue
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
		req.Header.Set("Authorization", "Bearer "+key)

		resp, err := r.client.Do(req)
		if err != nil {
			// Network-level failure: try the next candidate.
			lastErr = fmt.Errorf("provider %q transport error: %w", prov.Name, err)
			continue
		}
		if isRetryable(resp.StatusCode) {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("provider %q returned retryable status %d", prov.Name, resp.StatusCode)
			continue
		}
		return &Result{Model: m.Identifier, Provider: prov.Name, Response: resp}, intent, nil
	}

	if lastErr == nil {
		lastErr = ErrNoUpstream
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
