package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// DeepSeek's deserializer rejects assistant messages without an explicit
// content field (HTTP 422 "failed to deserialize the JSON body"). Empty
// assistant replies (nudge path, interrupts) used to serialize with the
// content key MISSING. Every message — including empty ones — must carry
// an explicit content string on the wire.
func TestDeepSeekAlwaysHasContentField(t *testing.T) {
	var body map[string]any
	srv := captureServer(t, &body)
	defer srv.Close()
	c := &openaiCompatible{name: "deepseek", apiKey: "k", base: srv.URL, model: "deepseek-v4-flash", http: srv.Client()}
	msgs := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: ""}, // the poison shape
		{Role: "user", Content: "still there?"},
	}
	s, err := c.Stream(context.Background(), "deepseek-v4-flash", msgs, nil)
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := s.Next(); err != nil {
			break
		}
	}
	_ = s.Close()
	raw, err := json.Marshal(body["messages"])
	if err != nil {
		t.Fatal(err)
	}
	var wire []map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for i, m := range wire {
		if _, ok := m["content"]; !ok {
			t.Fatalf("message %d (%s) missing content field — DeepSeek will 422: %s", i, m["role"], raw)
		}
	}
}

// DeepSeek thinking mode requires the LAST assistant tool-call round's
// reasoning_content to ride back with the tool results (HTTP 400 "The
// reasoning_content in the thinking mode must be passed back to the
// API"). Earlier rounds must NOT carry it, other vendors must NEVER see
// the field.
func TestDeepSeekReasoningPassedBack(t *testing.T) {
	var body map[string]any
	srv := captureServer(t, &body)
	defer srv.Close()
	c := &openaiCompatible{name: "deepseek", apiKey: "k", base: srv.URL, model: "deepseek-v4-flash", http: srv.Client()}
	msgs := []Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", Reasoning: "OLD", ToolCalls: []ToolCall{{ID: "a", Name: "f", Args: json.RawMessage("{}")}}},
		{Role: "tool", ToolCallID: "a", Content: "res"},
		{Role: "assistant", Reasoning: "NEW", ToolCalls: []ToolCall{{ID: "b", Name: "f", Args: json.RawMessage("{}")}}},
		{Role: "tool", ToolCallID: "b", Content: "res2"},
	}
	s, err := c.Stream(context.Background(), "deepseek-v4-flash", msgs, nil)
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := s.Next(); err != nil {
			break
		}
	}
	_ = s.Close()
	raw, _ := json.Marshal(body["messages"])
	var wire []map[string]any
	_ = json.Unmarshal(raw, &wire)
	if wire[3]["reasoning_content"] != "NEW" {
		t.Fatalf("last tool-call round must pass back its reasoning: %s", raw)
	}
	if _, leak := wire[1]["reasoning_content"]; leak {
		t.Fatalf("earlier rounds must NOT carry reasoning_content: %s", raw)
	}
	if _, leak := wire[4]["reasoning_content"]; leak {
		t.Fatalf("tool result must not carry reasoning_content: %s", raw)
	}
}

// The reasoning_content field must never reach vendors that reject it.
func TestOtherVendorsNoReasoningContent(t *testing.T) {
	for _, name := range []string{"openai", "openrouter", "gemini", "groq", "mistral"} {
		var body map[string]any
		srv := captureServer(t, &body)
		c := &openaiCompatible{name: name, apiKey: "k", base: srv.URL, model: "m", http: srv.Client()}
		msgs := []Message{
			{Role: "assistant", Reasoning: "thought hard", ToolCalls: []ToolCall{{ID: "a", Name: "f", Args: json.RawMessage("{}")}}},
			{Role: "tool", ToolCallID: "a", Content: "res"},
		}
		streamOnceMsgs(t, c, msgs)
		srv.Close()
		raw, _ := json.Marshal(body["messages"])
		if strings.Contains(string(raw), "reasoning_content") {
			t.Fatalf("%s payload leaked reasoning_content: %s", name, raw)
		}
	}
}

func streamOnceMsgs(t *testing.T, c *openaiCompatible, msgs []Message) {
	t.Helper()
	s, err := c.Stream(context.Background(), "m", msgs, nil)
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

// finish_reason "length" mid-tool-call must ERROR (not emit a truncated
// call): corrupted JSON arguments made the model retry in a silent
// token-burning loop. The error text is matched by the agent's nudge path.
func TestLengthCutErrorsInsteadOfTruncatedCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"apply_patch","arguments":"{\"patch\": \"*** Begin P"}}]}}}` + "\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()
	c := &openaiCompatible{name: "deepseek", apiKey: "k", base: srv.URL, model: "deepseek-v4-flash", http: srv.Client()}
	s, err := c.Stream(context.Background(), "deepseek-v4-flash", []Message{{Role: "user", Content: "x"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for {
		d, err := s.Next()
		if err != nil {
			if strings.Contains(err.Error(), "output was cut by the model's token cap") {
				return // correct: loud, no truncated call delivered
			}
			t.Fatalf("wrong error on length cut: %v", err)
		}
		if d.Call != nil {
			t.Fatal("truncated tool call was delivered as if valid")
		}
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
