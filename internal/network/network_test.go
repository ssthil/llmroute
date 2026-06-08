package network

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ssthil/llmroute/internal/database"
	"github.com/ssthil/llmroute/internal/router"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestHandleChatCompletionsEndToEnd drives the full handler: a chat request is
// classified, dispatched to a mocked upstream, the 200 response is relayed with
// llmroute headers, and a usage row is persisted and visible via Stats.
func TestHandleChatCompletionsEndToEnd(t *testing.T) {
	// Cheapest chat-capable model is deepseek-chat; give it a key.
	t.Setenv("DEEPSEEK_API_KEY", "test-key")

	db, err := database.Open(filepath.Join(t.TempDir(), "records.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var gotAuth string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`)),
		}, nil
	})}

	srv := NewServer(db, router.New(db, client), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hello there"}]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", resp.StatusCode, body)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("upstream Authorization = %q, want %q", gotAuth, "Bearer test-key")
	}
	if got := resp.Header.Get("X-LLMRoute-Model"); got != "deepseek-chat" {
		t.Errorf("X-LLMRoute-Model = %q, want deepseek-chat", got)
	}
	if got := resp.Header.Get("X-LLMRoute-Intent"); got != "chat" {
		t.Errorf("X-LLMRoute-Intent = %q, want chat", got)
	}
	if !strings.Contains(string(body), `"content":"hi"`) {
		t.Errorf("upstream body not relayed: %s", body)
	}

	stats, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if len(stats) != 1 || stats[0].Model != "deepseek-chat" || stats[0].Requests != 1 {
		t.Fatalf("usage not logged correctly: %+v", stats)
	}
}

// TestHandleChatCompletionsRejectsInvalidJSON confirms a malformed or
// non-object body is rejected with 400 before any upstream dispatch.
func TestHandleChatCompletionsRejectsInvalidJSON(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "records.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("upstream must not be called for an invalid body")
		return nil, nil
	})}
	srv := NewServer(db, router.New(db, client), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	cases := []struct {
		name string
		body string
	}{
		{"literal newline in string", "{\"messages\":[{\"content\":\"line1\nline2\"}]}"},
		{"not json", "not json at all"},
		{"json array not object", `[{"role":"user"}]`},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

// TestHandleChatCompletionsBlocksLeak confirms the credential gate fires before
// any upstream dispatch.
func TestHandleChatCompletionsBlocksLeak(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "records.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("upstream must not be called when a leak is detected")
		return nil, nil
	})}
	srv := NewServer(db, router.New(db, client), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"messages":[{"content":"key sk-abcdefghij1234567890ABCD"}]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestFindOpenPortReturnsFree(t *testing.T) {
	ln, port, err := FindOpenPort(DefaultPort)
	if err != nil {
		t.Fatalf("FindOpenPort: %v", err)
	}
	defer func() { _ = ln.Close() }()

	if port < DefaultPort {
		t.Errorf("port = %d, want >= %d", port, DefaultPort)
	}
	if got := ln.Addr().(*net.TCPAddr).IP.String(); got != loopback {
		t.Errorf("bound IP = %q, want %q", got, loopback)
	}
}

func TestFindOpenPortStepsUpWhenBusy(t *testing.T) {
	// Occupy a port, then confirm the scanner steps past it.
	busy, err := net.Listen("tcp", fmt.Sprintf("%s:0", loopback))
	if err != nil {
		t.Fatalf("seed listener: %v", err)
	}
	defer func() { _ = busy.Close() }()

	busyPort := busy.Addr().(*net.TCPAddr).Port

	ln, port, err := FindOpenPort(busyPort)
	if err != nil {
		t.Fatalf("FindOpenPort: %v", err)
	}
	defer func() { _ = ln.Close() }()

	if port == busyPort {
		t.Errorf("scanner returned busy port %d", busyPort)
	}
	if port <= busyPort {
		t.Errorf("port = %d, expected > busy port %d", port, busyPort)
	}
}
