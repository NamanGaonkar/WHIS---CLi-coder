package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// capture server: records the last request body, replies with a minimal SSE stream.
func captureServer(t *testing.T, body *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		m := map[string]any{}
		_ = json.Unmarshal(b, &m)
		*body = m
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
}

func streamOnce(t *testing.T, c *openaiCompatible) {
	t.Helper()
	s, err := c.Stream(context.Background(), "m", []Message{{Role: "user", Content: "x"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := s.Next(); err != nil {
			break
		}
	}
	_ = s.Close()
}

// DeepSeek's payload must KEEP its reasoning controls (user's main provider,
// explicitly protected from regressions).
func TestDeepSeekPayloadKeepsThinking(t *testing.T) {
	var body map[string]any
	srv := captureServer(t, &body)
	defer srv.Close()
	c := &openaiCompatible{name: "deepseek", apiKey: "k", base: srv.URL, model: "deepseek-v4-flash", http: srv.Client()}
	// wire model must say deepseek — that's the gate for thinking fields
	s, err := c.Stream(context.Background(), "deepseek-v4-flash", []Message{{Role: "user", Content: "x"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := s.Next(); err != nil {
			break
		}
	}
	_ = s.Close()
	if body["thinking"] == nil || body["reasoning_effort"] != "low" {
		t.Fatalf("deepseek thinking/effort lost: %+v", body)
	}
}

// Vendors OTHER than deepseek must never receive the deepseek-only fields
// (strict APIs reject unknown fields — the exact bug Gemini had).
func TestOtherVendorsNoDeepseekFields(t *testing.T) {
	for _, name := range []string{"openai", "openrouter", "gemini", "mistral", "moonshot", "qwen", "groq"} {
		var body map[string]any
		srv := captureServer(t, &body)
		c := &openaiCompatible{name: name, apiKey: "k", base: srv.URL, model: "m", http: srv.Client()}
		streamOnce(t, c)
		srv.Close()
		if body["thinking"] != nil || body["reasoning_effort"] != nil {
			t.Fatalf("%s payload leaked deepseek fields: %+v", name, body)
		}
		if body["max_tokens"] == nil {
			t.Fatalf("%s payload missing max_tokens", name)
		}
	}
}

// The shared /models parser used by 9 vendors: prefix filter + pricing.
func TestFetchOpenAIStyle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer K" {
			t.Errorf("want Bearer auth, got %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"grok-4","context_length":256000},
			{"id":"other-model","context_length":1000},
			{"id":"grok-mini","context_length":8192,"pricing":{"prompt":"0.0000015","completion":"0.000002"}}
		]}`))
	}))
	defer srv.Close()

	ms, err := fetchOpenAIStyle(srv.URL, "K", "grok", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 {
		t.Fatalf("prefix filter failed: %+v", ms)
	}
	if ms[0].ID != "grok-4" || ms[0].Context != 256000 {
		t.Fatalf("unexpected: %+v", ms[0])
	}
	if ms[1].In != 1.5 || ms[1].Out != 2.0 { // USD per million
		t.Fatalf("pricing parse wrong: %+v", ms[1])
	}
}
