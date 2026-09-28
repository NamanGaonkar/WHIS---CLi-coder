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

// Google's Gemini OpenAI-compat layer 400s on unknown root fields like
// max_output_tokens, and wants x-goog-api-key. This pins both.
func TestGeminiCompatPayloadAndAuth(t *testing.T) {
	var gotBody map[string]any
	var gotAuth, gotGoog string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		gotAuth = r.Header.Get("Authorization")
		gotGoog = r.Header.Get("x-goog-api-key")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := &openaiCompatible{name: "gemini", apiKey: "test-key-123", base: srv.URL, model: "gemini-2.5-flash", http: srv.Client()}
	s, err := c.Stream(context.Background(), "gemini-2.5-flash", []Message{{Role: "user", Content: "hey"}}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if _, err := s.Next(); err != nil {
		t.Fatalf("next: %v", err)
	}
	_ = s.Close()

	if _, bad := gotBody["max_output_tokens"]; bad {
		t.Fatal("payload contains max_output_tokens — Google rejects this field")
	}
	if v, ok := gotBody["max_tokens"]; !ok || v.(float64) <= 0 {
		t.Fatalf("payload missing/zero max_tokens: %v", gotBody["max_tokens"])
	}
	if !strings.HasPrefix(gotAuth, "Bearer test-key-123") {
		t.Fatalf("Bearer header wrong: %q", gotAuth)
	}
	if gotGoog != "test-key-123" {
		t.Fatalf("x-goog-api-key header missing/wrong: %q", gotGoog)
	}
}

func TestFetchGeminiModelsParsesNativeShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("x-goog-api-key") != "K" {
			t.Errorf("models fetch must use x-goog-api-key, got %q", r.Header.Get("x-goog-api-key"))
		}
		_, _ = w.Write([]byte(`{"models":[
			{"name":"models/gemini-2.5-flash","supportedGenerationMethods":["generateContent"],"inputTokenLimit":1048576},
			{"name":"models/gemini-2.5-pro","supportedGenerationMethods":["generateContent"],"inputTokenLimit":1048576},
			{"name":"models/text-embedding-004","supportedGenerationMethods":["embedContent"],"inputTokenLimit":2048}
		]}`))
	}))
	defer srv.Close()

	// point the fetcher at the test server
	old := geminiModelsURL
	geminiModelsURL = srv.URL + "/v1beta/models"
	defer func() { geminiModelsURL = old }()

	ms, err := fetchGeminiModels("K")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 {
		t.Fatalf("want 2 chat models (embedding row skipped), got %d: %+v", len(ms), ms)
	}
	if ms[0].Slug != "gemini:gemini-2.5-flash" || ms[0].Context != 1048576 {
		t.Fatalf("unexpected first model: %+v", ms[0])
	}
}
