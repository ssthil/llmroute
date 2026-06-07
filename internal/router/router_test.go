package router

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ssthil/llmroute/internal/database"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Intent
	}{
		{"plain chat", `{"messages":[{"role":"user","content":"what is the capital of France?"}]}`, IntentChat},
		{"code fence", "```go\nfunc main() {}\n```", IntentCode},
		{"python def", `please debug this def handler(): return None`, IntentCode},
		{"stack trace", "I got a traceback and an exception in my app", IntentCode},
		{"go file ext", "the bug is in network.go somewhere", IntentCode},
		{"image_url", `{"messages":[{"content":[{"type":"image_url","image_url":{}}]}]}`, IntentVision},
		{"base64 asset", `here is the data:image/png;base64,iVBORw0KG`, IntentVision},
		{"screenshot kw", "analyze this screenshot of my dashboard layout", IntentVision},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.body); got != tc.want {
				t.Errorf("Classify() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRewriteModel(t *testing.T) {
	in := []byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
	out, err := rewriteModel(in, "gpt-4o")
	if err != nil {
		t.Fatalf("rewriteModel: %v", err)
	}
	if !strings.Contains(string(out), `"model":"gpt-4o"`) {
		t.Errorf("model not rewritten: %s", out)
	}
	if !strings.Contains(string(out), `"messages"`) {
		t.Errorf("messages dropped: %s", out)
	}
}

func TestRewriteModelNonJSON(t *testing.T) {
	in := []byte("not json at all")
	out, err := rewriteModel(in, "gpt-4o")
	if err != nil {
		t.Fatalf("rewriteModel: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("opaque body changed: %s", out)
	}
}

func TestParseUsage(t *testing.T) {
	resp := []byte(`{"usage":{"prompt_tokens":12,"completion_tokens":34}}`)
	u := ParseUsage(resp)
	if u.PromptTokens != 12 || u.CompletionTokens != 34 {
		t.Errorf("ParseUsage = %+v", u)
	}
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("empty = %d, want 0", got)
	}
	if got := EstimateTokens("12345678"); got != 2 {
		t.Errorf("8 chars = %d, want 2", got)
	}
}

// roundTripFunc lets a test stand in for http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func newTestDB(t *testing.T) *database.DB {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := database.DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	db, err := database.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestDispatchFailover verifies the router skips a 429 provider and lands on
// the next candidate, returning its 200 response.
func TestDispatchFailover(t *testing.T) {
	db := newTestDB(t)

	var hits []string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hits = append(hits, r.URL.Host)
		// First provider tried is rate-limited; everything after succeeds.
		if len(hits) == 1 {
			return newResp(http.StatusTooManyRequests, `{"error":"rate limited"}`), nil
		}
		return newResp(http.StatusOK, `{"ok":true}`), nil
	})}

	rtr := New(db, client)
	// All providers have keys so none are skipped.
	rtr.keyFn = func(string) string { return "test-key" }

	// A code request: candidates are deepseek/anthropic/openai/gemini-pro.
	body := []byte("```go\nfunc main(){}\n```")
	res, intent, err := rtr.Dispatch(context.Background(), body)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	defer func() { _ = res.Response.Body.Close() }()

	if intent != IntentCode {
		t.Errorf("intent = %q, want code", intent)
	}
	if res.Response.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.Response.StatusCode)
	}
	if len(hits) < 2 {
		t.Errorf("expected failover to a second provider, got %d attempt(s)", len(hits))
	}
}

// TestDispatchSkipsMissingKeys verifies providers without an env key are
// skipped, and that exhausting all candidates returns ErrNoUpstream.
func TestDispatchSkipsMissingKeys(t *testing.T) {
	db := newTestDB(t)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("transport should not be called when no keys are set")
		return nil, nil
	})}
	rtr := New(db, client)
	rtr.keyFn = func(string) string { return "" }

	_, _, err := rtr.Dispatch(context.Background(), []byte(`{"messages":[]}`))
	if err == nil {
		t.Fatal("expected error when no provider keys are configured")
	}
}
