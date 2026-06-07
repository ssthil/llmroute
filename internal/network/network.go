// Package network hosts the loopback reverse-proxy listener and the port-scan
// resiliency engine that finds a free local port to bind.
package network

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/ssthil/llmroute/internal/database"
	"github.com/ssthil/llmroute/internal/router"
	"github.com/ssthil/llmroute/internal/security"
)

// DefaultPort is where the proxy prefers to bind.
const DefaultPort = 4040

// loopback is the only interface the proxy ever binds to.
const loopback = "127.0.0.1"

// maxPortProbes bounds the upward scan so a fully saturated range fails fast
// instead of looping toward 65535.
const maxPortProbes = 64

// maxBodyBytes caps the inbound payload we will buffer for inspection.
const maxBodyBytes = 32 << 20 // 32 MiB

// FindOpenPort scans upward from start on the loopback interface and returns
// the first bound listener it can open. It steps 4040 -> 4041 -> ... until it
// succeeds or exhausts maxPortProbes.
func FindOpenPort(start int) (net.Listener, int, error) {
	for port := start; port < start+maxPortProbes && port <= 65535; port++ {
		addr := fmt.Sprintf("%s:%d", loopback, port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			var opErr *net.OpError
			// Address already in use (or otherwise unavailable): step up.
			if errors.As(err, &opErr) {
				continue
			}
			return nil, 0, fmt.Errorf("listen on %s: %w", addr, err)
		}
		return ln, port, nil
	}
	return nil, 0, fmt.Errorf("no free port in range %d-%d on %s", start, start+maxPortProbes-1, loopback)
}

// Server is the llmroute proxy gateway.
type Server struct {
	db     *database.DB
	router *router.Router
	logger *log.Logger
}

// NewServer wires the proxy handler to its database and router.
func NewServer(db *database.DB, rtr *router.Router, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.Default()
	}
	return &Server{db: db, router: rtr, logger: logger}
}

// Handler builds the HTTP mux exposing the proxy endpoints.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	return mux
}

// ListenAndServe binds the first free port at/above startPort on loopback and
// serves until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, startPort int) error {
	ln, port, err := FindOpenPort(startPort)
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	s.logger.Printf("llmroute proxy listening on http://%s:%d", loopback, port)
	s.logger.Printf("point your client at http://%s:%d/v1/chat/completions", loopback, port)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// handleChatCompletions consumes an OpenAI chat payload, screens it for leaked
// credentials, routes it by intent with failover, and pipes the upstream
// response back to the caller.
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	// Gate: refuse to forward anything that looks like a live secret.
	if leak := security.ScanForKeys(string(body)); leak != nil {
		s.logger.Printf("blocked request: %v", leak)
		writeError(w, http.StatusBadRequest, leak.Error())
		return
	}

	result, intent, err := s.router.Dispatch(r.Context(), body)
	if err != nil {
		s.logger.Printf("dispatch failed (intent=%s): %v", intent, err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer func() { _ = result.Response.Body.Close() }()

	s.logger.Printf("routed intent=%s -> %s/%s (status %d)",
		intent, result.Provider, result.Model, result.Response.StatusCode)

	// Mirror upstream headers and status to the caller.
	copyHeader(w.Header(), result.Response.Header)
	w.Header().Set("X-LLMRoute-Model", result.Model)
	w.Header().Set("X-LLMRoute-Intent", string(intent))
	w.WriteHeader(result.Response.StatusCode)

	// Stream the response back while counting completion bytes for accounting.
	counter := &countingWriter{w: w}
	if f, ok := w.(http.Flusher); ok {
		counter.flusher = f
	}
	if _, err := io.Copy(counter, result.Response.Body); err != nil {
		s.logger.Printf("stream copy interrupted: %v", err)
	}

	s.recordUsage(body, result, counter.n)
}

// recordUsage logs token accounting using a character-based estimate
// (~4 chars/token) for both the prompt and the streamed response volume.
func (s *Server) recordUsage(reqBody []byte, result *router.Result, respBytes int64) {
	promptTokens := router.EstimateTokens(string(reqBody))
	completionTokens := int((respBytes + 3) / 4)

	if err := s.db.LogUsage(result.Model, promptTokens, completionTokens); err != nil {
		s.logger.Printf("usage log failed for %s: %v", result.Model, err)
	}
}

// countingWriter counts bytes written and flushes when the underlying writer
// supports it (so streamed chunks reach the client promptly).
type countingWriter struct {
	w       io.Writer
	flusher http.Flusher
	n       int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if c.flusher != nil {
		c.flusher.Flush()
	}
	return n, err
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		// Skip hop-by-hop and length headers; the proxy re-derives them.
		switch k {
		case "Content-Length", "Connection", "Transfer-Encoding":
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, fmt.Sprintf(`{"error":{"message":%q,"type":"llmroute_error"}}`, msg))
}
